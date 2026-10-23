package metrics_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mic92/gitea-mq/internal/metrics"
	"github.com/Mic92/gitea-mq/internal/queue"
)

func TestRegistry(t *testing.T) {
	r := metrics.New()
	r.Observe("gitea:org/app", queue.Event{Kind: queue.EventEjected, Reason: "check_failed"})
	r.Observe("gitea:org/app", queue.Event{Kind: queue.EventEjected, Reason: "check_failed"})
	r.Observe("gitea:org/app", queue.Event{Kind: queue.EventMerged, Reason: "merged"})
	r.Observe(`gitea:o/"x"`, queue.Event{Kind: queue.EventEnqueued})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE gitea_mq_events_total counter",
		`gitea_mq_events_total{repo="gitea:org/app",kind="ejected",reason="check_failed"} 2`,
		`gitea_mq_events_total{repo="gitea:org/app",kind="merged",reason="merged"} 1`,
		`gitea_mq_events_total{repo="gitea:o/\"x\"",kind="enqueued",reason=""} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
