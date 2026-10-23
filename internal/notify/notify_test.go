package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/notify"
	"github.com/Mic92/gitea-mq/internal/queue"
)

func TestNotifier(t *testing.T) {
	secret := []byte("s3cret")
	got := make(chan notify.Payload, 4)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First delivery fails with 503 to exercise the retry.
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get(notify.SignatureHeader) != notify.Sign(secret, body) {
			t.Errorf("bad signature")
		}
		if r.Header.Get(notify.EventHeader) != "ejected" {
			t.Errorf("event header = %q", r.Header.Get(notify.EventHeader))
		}
		var p notify.Payload
		_ = json.Unmarshal(body, &p)
		got <- p
	}))
	defer srv.Close()

	n := notify.New(srv.URL, secret, []string{"ejected"})
	n.Backoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)

	n.Observe("gitea:org/app", queue.Event{Kind: queue.EventEnqueued, PRNumber: 1}) // filtered out
	n.Observe("gitea:org/app", queue.Event{
		ID: 7, Kind: queue.EventEjected, PRNumber: 2, Reason: "check_failed",
		Detail: map[string]any{"check": "ci", "target_url": "https://ci/1"},
	})

	select {
	case p := <-got:
		if p.ID != 7 || p.Repo != "gitea:org/app" || p.PR != 2 || p.Reason != "check_failed" || p.Detail["target_url"] != "https://ci/1" {
			t.Fatalf("payload = %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one retry, enqueued filtered)", calls.Load())
	}
}
