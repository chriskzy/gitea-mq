package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/testutil"
	"github.com/Mic92/gitea-mq/internal/web"
)

func TestEventsAPI(t *testing.T) {
	svc, ctx, repoID := testutil.TestQueueService(t)
	lookup := func(key string) (int64, bool) { return repoID, key == "gitea:org/app" }

	for _, e := range []queue.Event{
		{Kind: queue.EventEnqueued, PRNumber: 1},
		{Kind: queue.EventEjected, PRNumber: 1, Reason: "check_failed", Detail: map[string]any{"check": "ci"}},
		{Kind: queue.EventEnqueued, PRNumber: 2},
	} {
		e.RepoID = repoID
		svc.RecordEvent(ctx, e)
	}

	get := func(h http.Handler, url string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	events := web.EventsHandler(svc, lookup)

	code, body := get(events, "/api/events?repo=gitea:org/app&kind=ejected")
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	evs := body["events"].([]any)
	if len(evs) != 1 {
		t.Fatalf("kind filter: %v", evs)
	}
	ev := evs[0].(map[string]any)
	if ev["repo"] != "gitea:org/app" || ev["reason"] != "check_failed" || ev["pr"] != float64(1) ||
		ev["detail"].(map[string]any)["check"] != "ci" {
		t.Fatalf("event = %v", ev)
	}

	// Cursor paging.
	_, body = get(events, "/api/events?limit=2")
	if n := len(body["events"].([]any)); n != 2 {
		t.Fatalf("limit: %d events", n)
	}
	after := body["next_after"].(float64)
	_, body = get(events, "/api/events?after="+jsonNum(after))
	if n := len(body["events"].([]any)); n != 1 {
		t.Fatalf("after cursor: %d events", n)
	}

	if code, _ := get(events, "/api/events?repo=gitea:other/x"); code != http.StatusNotFound {
		t.Fatalf("unknown repo: code=%d", code)
	}
	if code, _ := get(events, "/api/events?limit=-1"); code != http.StatusBadRequest {
		t.Fatalf("bad limit: code=%d", code)
	}

	_, body = get(web.EventSummaryHandler(svc, lookup), "/api/events/summary?since=1h")
	counts := map[string]float64{}
	for _, c := range body["counts"].([]any) {
		m := c.(map[string]any)
		reason, _ := m["reason"].(string)
		counts[m["kind"].(string)+":"+reason] = m["count"].(float64)
	}
	if counts["enqueued:"] != 2 || counts["ejected:check_failed"] != 1 {
		t.Fatalf("summary = %v", counts)
	}
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(int64(f))
	return string(b)
}

func TestRepoScopedAPI(t *testing.T) {
	svc, ctx, repoID := testutil.TestQueueService(t)
	lookup := func(key string) (int64, bool) { return repoID, key == "gitea:org/app" }
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/repos/{forge}/{owner}/{name}/events", web.EventsHandler(svc, lookup))
	mux.Handle("GET /api/v1/repos/{forge}/{owner}/{name}/queue", web.QueueHandler(svc, lookup))

	for _, pr := range []int64{1, 2} {
		if _, err := svc.Enqueue(ctx, repoID, pr, "sha", "main", false); err != nil {
			t.Fatal(err)
		}
	}
	testutil.EnqueueTesting(t, svc, repoID, 3, "sha3", "merge3")
	svc.RecordEvent(ctx, queue.Event{RepoID: repoID, Kind: queue.EventEnqueued, PRNumber: 1})

	get := func(url string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	code, body := get("/api/v1/repos/gitea/org/app/events?since=1h")
	if code != http.StatusOK || len(body["events"].([]any)) != 1 {
		t.Fatalf("events: code=%d body=%v", code, body)
	}

	code, body = get("/api/v1/repos/gitea/org/app/queue")
	if code != http.StatusOK {
		t.Fatalf("queue: code=%d", code)
	}
	main := body["branches"].(map[string]any)["main"].([]any)
	if len(main) != 3 {
		t.Fatalf("queue main = %v", main)
	}
	// In-flight entries sort first.
	head := main[0].(map[string]any)
	if head["pr"] != float64(3) || head["state"] != "testing" || head["position"] != float64(1) || head["merge_branch_sha"] != "merge3" {
		t.Fatalf("head = %v", head)
	}

	if code, _ := get("/api/v1/repos/gitea/other/repo/queue"); code != http.StatusNotFound {
		t.Fatalf("unmanaged repo: code=%d", code)
	}
}

// Observers (notifications) carry the stored id so consumers can resume
// with /api/events?after=<id>.
func TestRecordEventObserverGetsID(t *testing.T) {
	svc, ctx, repoID := testutil.TestQueueService(t)
	var got queue.Event
	svc.OnEvent(func(_ string, e queue.Event) { got = e })
	svc.RecordEvent(ctx, queue.Event{RepoID: repoID, Kind: queue.EventEjected, PRNumber: 5})
	evs, err := svc.ListEvents(ctx, queue.EventFilter{RepoID: repoID, Limit: 1})
	if err != nil || len(evs) != 1 {
		t.Fatalf("list: %v %v", evs, err)
	}
	if got.ID != evs[0].ID || got.Time.IsZero() {
		t.Fatalf("observer event id=%d time=%v, stored id=%d", got.ID, got.Time, evs[0].ID)
	}
}
