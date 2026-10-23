package metrics_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestRegistry_QueueDuration(t *testing.T) {
	r := metrics.New()
	now := time.Now()
	r.Observe("gitea:org/app", queue.Event{Kind: queue.EventMerged, EnqueuedAt: now.Add(-90 * time.Second)})
	r.Observe("gitea:org/app", queue.Event{Kind: queue.EventMerged, EnqueuedAt: now.Add(-2 * time.Hour)})
	r.Observe("gitea:org/app", queue.Event{Kind: queue.EventEnqueued}) // no duration

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE gitea_mq_queue_duration_seconds histogram",
		`gitea_mq_queue_duration_seconds_bucket{repo="gitea:org/app",outcome="merged",le="60"} 0`,
		`gitea_mq_queue_duration_seconds_bucket{repo="gitea:org/app",outcome="merged",le="300"} 1`,
		`gitea_mq_queue_duration_seconds_bucket{repo="gitea:org/app",outcome="merged",le="7200"} 1`,
		`gitea_mq_queue_duration_seconds_bucket{repo="gitea:org/app",outcome="merged",le="14400"} 2`,
		`gitea_mq_queue_duration_seconds_bucket{repo="gitea:org/app",outcome="merged",le="+Inf"} 2`,
		`gitea_mq_queue_duration_seconds_count{repo="gitea:org/app",outcome="merged"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, `outcome="enqueued"`) {
		t.Errorf("enqueued must not produce a duration:\n%s", body)
	}
}
