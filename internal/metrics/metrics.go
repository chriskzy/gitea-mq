// Package metrics keeps in-memory counters of queue events and serves them in
// the Prometheus text format. Counters reset on restart; the queue_events
// table (GET /api/events) is the durable record.
package metrics

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mic92/gitea-mq/internal/queue"
)

type key struct{ repo, kind, reason string }

// durationBuckets are the upper bounds (seconds) of the queue-time
// histogram: one minute to one day.
var durationBuckets = []float64{60, 300, 900, 1800, 3600, 7200, 14400, 43200, 86400}

type histKey struct{ repo, outcome string }

type histogram struct {
	counts []uint64 // per bucket, cumulative on output
	sum    float64
	count  uint64
}

// Registry counts queue events by repo, kind and reason, and records how
// long PRs spent in the queue by outcome.
type Registry struct {
	mu        sync.Mutex
	events    map[key]uint64
	durations map[histKey]*histogram
	now       func() time.Time
}

func New() *Registry {
	return &Registry{events: map[key]uint64{}, durations: map[histKey]*histogram{}, now: time.Now}
}

// Observe is a queue.EventObserver.
func (r *Registry) Observe(repo string, e queue.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[key{repo, string(e.Kind), e.Reason}]++
	d := e.QueuedFor(r.now())
	if d <= 0 {
		return
	}
	hk := histKey{repo, string(e.Kind)}
	h := r.durations[hk]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(durationBuckets))}
		r.durations[hk] = h
	}
	secs := d.Seconds()
	for i, ub := range durationBuckets {
		if secs <= ub {
			h.counts[i]++
			break
		}
	}
	h.sum += secs
	h.count++
}

// ServeHTTP writes gitea_mq_events_total in the Prometheus text format.
func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	keys := make([]key, 0, len(r.events))
	for k := range r.events {
		keys = append(keys, k)
	}
	counts := make(map[key]uint64, len(keys))
	for _, k := range keys {
		counts[k] = r.events[k]
	}
	hkeys := make([]histKey, 0, len(r.durations))
	hists := make(map[histKey]histogram, len(r.durations))
	for k, h := range r.durations {
		hkeys = append(hkeys, k)
		hists[k] = histogram{counts: slices.Clone(h.counts), sum: h.sum, count: h.count}
	}
	r.mu.Unlock()
	slices.SortFunc(hkeys, func(a, b histKey) int {
		return strings.Compare(a.repo+"\x00"+a.outcome, b.repo+"\x00"+b.outcome)
	})

	slices.SortFunc(keys, func(a, b key) int {
		return strings.Compare(a.repo+"\x00"+a.kind+"\x00"+a.reason, b.repo+"\x00"+b.kind+"\x00"+b.reason)
	})

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder
	b.WriteString("# HELP gitea_mq_events_total Queue events recorded since process start.\n")
	b.WriteString("# TYPE gitea_mq_events_total counter\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "gitea_mq_events_total{repo=%s,kind=%s,reason=%s} %d\n",
			quote(k.repo), quote(k.kind), quote(k.reason), counts[k])
	}
	b.WriteString("# HELP gitea_mq_queue_duration_seconds Time from enqueue until a PR left the queue, by outcome.\n")
	b.WriteString("# TYPE gitea_mq_queue_duration_seconds histogram\n")
	for _, k := range hkeys {
		h := hists[k]
		labels := fmt.Sprintf("repo=%s,outcome=%s", quote(k.repo), quote(k.outcome))
		var cum uint64
		for i, ub := range durationBuckets {
			cum += h.counts[i]
			fmt.Fprintf(&b, "gitea_mq_queue_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(ub, 'f', -1, 64), cum)
		}
		fmt.Fprintf(&b, "gitea_mq_queue_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, h.count)
		fmt.Fprintf(&b, "gitea_mq_queue_duration_seconds_sum{%s} %s\n", labels, strconv.FormatFloat(h.sum, 'f', -1, 64))
		fmt.Fprintf(&b, "gitea_mq_queue_duration_seconds_count{%s} %d\n", labels, h.count)
	}
	_, _ = w.Write([]byte(b.String()))
}

// quote escapes a label value per the Prometheus text format.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
