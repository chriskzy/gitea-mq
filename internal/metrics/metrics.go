// Package metrics keeps in-memory counters of queue events and serves them in
// the Prometheus text format. Counters reset on restart; the queue_events
// table (GET /api/events) is the durable record.
package metrics

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/Mic92/gitea-mq/internal/queue"
)

type key struct{ repo, kind, reason string }

// Registry counts queue events by repo, kind and reason.
type Registry struct {
	mu     sync.Mutex
	events map[key]uint64
}

func New() *Registry { return &Registry{events: map[key]uint64{}} }

// Observe is a queue.EventObserver.
func (r *Registry) Observe(repo string, e queue.Event) {
	r.mu.Lock()
	r.events[key{repo, string(e.Kind), e.Reason}]++
	r.mu.Unlock()
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
	r.mu.Unlock()

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
	_, _ = w.Write([]byte(b.String()))
}

// quote escapes a label value per the Prometheus text format.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
