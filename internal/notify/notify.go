// Package notify pushes queue events to an external HTTP endpoint
// (GITEA_MQ_NOTIFY_URL), so a consumer such as a dispatcher that tells
// authors about ejections can react within seconds instead of polling
// /api/events. Delivery is best-effort: events are queued in memory, sent
// with a few retries, and dropped when the receiver stays down. The events
// table remains the durable record; a consumer that missed deliveries
// catches up with /api/events?after=<last id>.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/Mic92/gitea-mq/internal/queue"
)

// SignatureHeader carries hex(HMAC-SHA256(secret, body)), prefixed with
// "sha256=" like GitHub's X-Hub-Signature-256.
const SignatureHeader = "X-Gitea-MQ-Signature"

// EventHeader carries the event kind, for routing without parsing the body.
const EventHeader = "X-Gitea-MQ-Event"

// Payload is the JSON body of a delivery; it matches /api/events entries.
type Payload struct {
	ID           int64          `json:"id"`
	Time         time.Time      `json:"time"`
	Repo         string         `json:"repo"`
	Kind         string         `json:"kind"`
	Reason       string         `json:"reason,omitempty"`
	PR           int64          `json:"pr,omitempty"`
	Batch        int64          `json:"batch,omitempty"`
	TargetBranch string         `json:"target_branch,omitempty"`
	HeadSHA      string         `json:"head_sha,omitempty"`
	Detail       map[string]any `json:"detail,omitempty"`
}

// Notifier delivers events to URL. Create it with New and run Run.
type Notifier struct {
	URL    string
	Secret []byte
	// Kinds limits deliveries to these event kinds; empty sends all.
	Kinds []string

	Client   *http.Client
	Attempts int
	Backoff  time.Duration

	ch chan Payload
}

// New returns a notifier with a bounded in-memory queue.
func New(url string, secret []byte, kinds []string) *Notifier {
	return &Notifier{
		URL:      url,
		Secret:   secret,
		Kinds:    kinds,
		Client:   &http.Client{Timeout: 10 * time.Second},
		Attempts: 3,
		Backoff:  time.Second,
		ch:       make(chan Payload, 256),
	}
}

// Observe is a queue.EventObserver. It never blocks the queue: when the
// buffer is full the event is dropped with a warning.
func (n *Notifier) Observe(repo string, e queue.Event) {
	if len(n.Kinds) > 0 && !slices.Contains(n.Kinds, string(e.Kind)) {
		return
	}
	p := Payload{
		ID: e.ID, Time: e.Time, Repo: repo, Kind: string(e.Kind), Reason: e.Reason,
		PR: e.PRNumber, Batch: e.BatchID, TargetBranch: e.TargetBranch, HeadSHA: e.HeadSHA, Detail: e.Detail,
	}
	select {
	case n.ch <- p:
	default:
		slog.Warn("notify: queue full, dropping event", "id", e.ID, "kind", e.Kind, "pr", e.PRNumber)
	}
}

// Run delivers queued events until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-n.ch:
			if err := n.deliver(ctx, p); err != nil {
				slog.Warn("notify: delivery failed, dropping event", "id", p.ID, "kind", p.Kind, "error", err)
			}
		}
	}
}

// Sign returns the SignatureHeader value for body.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (n *Notifier) deliver(ctx context.Context, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var last error
	for attempt := range max(n.Attempts, 1) {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(n.Backoff << (attempt - 1)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(EventHeader, p.Kind)
		if len(n.Secret) > 0 {
			req.Header.Set(SignatureHeader, Sign(n.Secret, body))
		}
		resp, err := n.Client.Do(req)
		if err != nil {
			last = err
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		last = fmt.Errorf("status %d", resp.StatusCode)
		// A 4xx other than 429 will not get better by retrying.
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return last
		}
	}
	return last
}
