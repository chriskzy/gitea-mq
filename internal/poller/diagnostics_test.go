package poller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/failure"
	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/monitor"
	"github.com/Mic92/gitea-mq/internal/poller"
	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/store/pg"
	"github.com/Mic92/gitea-mq/internal/testutil"
	"github.com/Mic92/gitea-mq/internal/web"
	"github.com/Mic92/gitea-mq/internal/webhook"
)

type diagnosisForge struct {
	*forge.MockForge
	log      string
	logCalls int
}

type diagnosisRepos struct{}

func (diagnosisRepos) List() []forge.RepoRef {
	return []forge.RepoRef{{Forge: forge.KindGitea, Owner: "org", Name: "app"}}
}
func (diagnosisRepos) Contains(key string) bool { return key == "gitea:org/app" }

func (f *diagnosisForge) JobLog(context.Context, string, string, string) (string, []byte, error) {
	f.logCalls++
	return "unit tests", []byte(f.log), nil
}

func TestPreAdmissionDiagnosesAreGroupedDurableAndInvalidated(t *testing.T) {
	svc, ctx, repoID := testutil.TestQueueService(t)
	prs := []forge.PR{{Number: 1, HeadSHA: "head1", BaseBranch: "main"}, {Number: 2, HeadSHA: "head2", BaseBranch: "main"}}
	state := pg.CheckStateFailure
	unavailable := false
	f := &diagnosisForge{MockForge: &forge.MockForge{}, log: "--- FAIL: TestThing (0.01s)\nAssertionError: expected 2 got 1\nTOKEN=supersecret"}
	f.ListOpenPRsFn = func(context.Context, string, string) ([]forge.PR, error) { return prs, nil }
	f.GetCheckStatesFn = func(_ context.Context, _, _, sha string) (map[string]forge.Check, error) {
		if unavailable {
			return nil, fmt.Errorf("API unavailable")
		}
		return map[string]forge.Check{"ci/unit": {State: state, Description: "failed", TargetURL: "https://ci/" + sha}}, nil
	}
	deps := &poller.Deps{Forge: f, Queue: svc, RepoID: repoID, Owner: "org", Repo: "app", ChecksFromConfig: true, FallbackChecks: []string{"ci/unit"}, PRHeadDiagnostics: true, DiagnosisHeads: &failure.HeadTracker{}, FailureDetails: failure.Options{LogLines: 20, Secrets: []string{"supersecret"}}}
	poll := func() {
		t.Helper()
		r, err := poller.PollOnce(ctx, deps)
		if err != nil || len(r.Errors) > 0 {
			t.Fatalf("poll=%+v err=%v", r, err)
		}
		if len(r.Enqueued) > 0 {
			t.Fatal("observation enqueued PR without intent")
		}
	}
	api := web.HeadFailuresHandler(svc, func(key string) (int64, bool) { return repoID, key == "gitea:org/app" }, true)
	get := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/head-failures?repo=gitea:org/app", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "supersecret") {
			t.Fatal("secret persisted in API")
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// An unmatched PR-head status must wake diagnosis before queue admission.
	triggered := 0
	deps.DiagnosisHeads.Update(prs)
	hook := webhook.Handler("hook-secret", webhook.MapRepoLookup{"gitea:org/app": &webhook.RepoMonitor{Deps: &monitor.Deps{RepoID: repoID, FailureDetails: deps.FailureDetails}, DiagnosisHeads: deps.DiagnosisHeads, TriggerPoll: func() { triggered++; poll() }}}, svc)
	postStatus := func(check string) {
		t.Helper()
		body := `{"sha":"head1","context":"` + check + `","state":"failure","repository":{"full_name":"org/app"}}`
		r := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
		r.Header.Set("X-Gitea-Event", "status")
		r.Header.Set("X-Gitea-Signature", webhook.ComputeSignature([]byte(body), "hook-secret"))
		w := httptest.NewRecorder()
		hook.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	postStatus("ci/unit")
	postStatus("gitea-mq/ci/unit")
	if triggered != 1 {
		t.Fatalf("status wake or own-context loop: %d", triggered)
	}
	groups := get()["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups=%v", groups)
	}
	g := groups[0].(map[string]any)
	if g["count"] != float64(2) || g["classification"] != "source" || g["test"] != "TestThing" {
		t.Fatalf("group=%v", g)
	}
	dashboard := httptest.NewRecorder()
	web.NewMux(&web.Deps{Queue: svc, PRHeadDiagnostics: true, Repos: diagnosisRepos{}}).ServeHTTP(dashboard, httptest.NewRequest(http.MethodGet, "/repo/gitea/org/app", nil))
	for _, want := range []string{"PR head CI failures", "TestThing", "source", "head1"} {
		if dashboard.Code != 200 || !strings.Contains(dashboard.Body.String(), want) {
			t.Fatalf("dashboard missing %q: %d %s", want, dashboard.Code, dashboard.Body.String())
		}
	}
	// Copying deps simulates a fresh poller; deduplication must live in the DB.
	restarted := *deps
	deps = &restarted
	poll()
	if f.logCalls != 2 {
		t.Fatalf("restart repeated log reads: %d", f.logCalls)
	}
	events, err := svc.ListEvents(ctx, queue.EventFilter{RepoID: repoID, Kind: queue.EventPRHeadFailed, Limit: 100})
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	// A same-URL rerun whose pending window was missed is re-read after expiry.
	f.log = "--- FAIL: TestThing (0.01s)\nAssertionError: expected 3 got 1"
	deps.Now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	poll()
	if f.logCalls != 4 {
		t.Fatalf("expired evidence not refreshed: %d", f.logCalls)
	}
	newGroup := get()["groups"].([]any)[0].(map[string]any)
	if newGroup["fingerprint"] == g["fingerprint"] {
		t.Fatal("changed assertion retained old fingerprint")
	}
	poll()
	events, err = svc.ListEvents(ctx, queue.EventFilter{RepoID: repoID, Kind: queue.EventPRHeadFailed, Limit: 100})
	if err != nil || len(events) != 4 {
		t.Fatalf("unchanged refresh emitted event: %d %v", len(events), err)
	}
	deps.Now = nil
	unavailable = true
	r, err := poller.PollOnce(ctx, deps)
	if err != nil || len(r.Errors) != 2 {
		t.Fatalf("API failure not reported: %+v %v", r, err)
	}
	members := get()["groups"].([]any)[0].(map[string]any)["members"].([]any)
	if members[0].(map[string]any)["stale"] != true {
		t.Fatal("API failure retained fresh diagnosis")
	}
	unavailable = false
	// New head invalidates old evidence even when the replacement is pending;
	// a closed PR's evidence disappears in the same reconcile.
	prs = prs[:1]
	prs[0].HeadSHA = "replacement"
	state = pg.CheckStatePending
	poll()
	if len(get()["groups"].([]any)) != 0 {
		t.Fatal("pushed/closed diagnosis survived")
	}
	state = pg.CheckStateFailure
	poll()
	state = pg.CheckStateSuccess
	poll()
	if len(get()["groups"].([]any)) != 0 {
		t.Fatal("recovered failure survived")
	}
	for _, method := range []string{"Comment", "SetMQStatus", "MirrorCheck", "MergePR", "CancelAutoMerge"} {
		if len(f.CallsTo(method)) > 0 {
			t.Fatalf("observer mutated forge: %s", method)
		}
	}
}

func TestDiagnosisObservationBudgetRotates(t *testing.T) {
	svc, ctx, repoID := testutil.TestQueueService(t)
	prs := make([]forge.PR, 33)
	for i := range prs {
		prs[i] = forge.PR{Number: int64(i + 1), HeadSHA: fmt.Sprint(i + 1), BaseBranch: "main"}
		if i < 10 {
			prs[i].BaseBranch = "excluded"
		}
	}
	f := &forge.MockForge{ListOpenPRsFn: func(context.Context, string, string) ([]forge.PR, error) { return prs, nil }, GetCheckStatesFn: func(context.Context, string, string, string) (map[string]forge.Check, error) {
		return map[string]forge.Check{"ci": {State: pg.CheckStateFailure}}, nil
	}}
	d := &poller.Deps{Forge: f, Queue: svc, RepoID: repoID, Owner: "org", Repo: "app", TargetBranches: []string{"main"}, ChecksFromConfig: true, PRHeadDiagnostics: true, DiagnosisHeads: &failure.HeadTracker{}, FailureDetails: failure.Options{Enabled: true}}
	for i := range 3 {
		r, err := poller.PollOnce(ctx, d)
		if err != nil || len(r.Errors) > 0 {
			t.Fatalf("%+v %v", r, err)
		}
		if len(f.CallsTo("GetCheckStates")) != (i+1)*10 {
			t.Fatal("head read budget exceeded")
		}
	}
	heads, err := svc.HeadDiagnoses(ctx, repoID)
	if err != nil || len(heads) != 23 {
		t.Fatalf("rotation starved PRs: %d %v", len(heads), err)
	}
}

func TestLegacyFailureDetailsDoesNotPublishOrObservePRHeads(t *testing.T) {
	svc, ctx, id := testutil.TestQueueService(t)
	f := &forge.MockForge{ListOpenPRsFn: func(context.Context, string, string) ([]forge.PR, error) {
		return []forge.PR{{Number: 1, HeadSHA: "head", BaseBranch: "main"}}, nil
	}}
	d := &poller.Deps{Forge: f, Queue: svc, RepoID: id, Owner: "org", Repo: "app", FailureDetails: failure.Options{Enabled: true}}
	r, err := poller.PollOnce(ctx, d)
	if err != nil || len(r.Errors) > 0 || len(f.CallsTo("GetCheckStates")) != 0 {
		t.Fatalf("legacy flag added observation: %+v %v", r, err)
	}
	_, err = svc.SaveHeadDiagnosis(ctx, id, queue.HeadDiagnosis{PR: 1, HeadSHA: "head", TargetBranch: "main", Failures: []failure.Diagnosis{{Check: failure.Check{Context: "private-ci-content"}}}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	web.HeadFailuresHandler(svc, func(string) (int64, bool) { return id, true }, false).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/head-failures", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled endpoint public: %d", w.Code)
	}
	w = httptest.NewRecorder()
	web.NewMux(&web.Deps{Queue: svc, Repos: diagnosisRepos{}}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/repo/gitea/org/app", nil))
	if strings.Contains(w.Body.String(), "private-ci-content") {
		t.Fatal("disabled dashboard exposed retained CI content")
	}
}

func TestDiagnosisExcludesDraftAndExplicitlyUngatedHeads(t *testing.T) {
	for _, ungated := range []bool{false, true} {
		svc, ctx, id := testutil.TestQueueService(t)
		pr := forge.PR{Number: 1, HeadSHA: "head", BaseBranch: "main", Draft: !ungated}
		f := &forge.MockForge{ListOpenPRsFn: func(context.Context, string, string) ([]forge.PR, error) { return []forge.PR{pr}, nil }}
		heads := &failure.HeadTracker{}
		d := &poller.Deps{Forge: f, Queue: svc, RepoID: id, Owner: "org", Repo: "app", PRHeadDiagnostics: true, DiagnosisHeads: heads}
		if ungated {
			empty := []string{}
			d.PRChecks = &empty
		}
		if _, err := poller.PollOnce(ctx, d); err != nil {
			t.Fatal(err)
		}
		if len(f.CallsTo("GetCheckStates")) != 0 || heads.Contains("head") {
			t.Fatal("ineligible head observed or allowed webhook wakeup")
		}
	}
}

func TestDiagnosisRunsAfterMergeBranchCheckProcessing(t *testing.T) {
	svc, ctx, id := testutil.TestQueueService(t)
	queued := forge.PR{Number: 42, HeadSHA: "head42", BaseBranch: "main", AutoMergeEnabled: true}
	failed := forge.PR{Number: 1, HeadSHA: "failed-head", BaseBranch: "main"}
	mergeObserved := false
	f := &forge.MockForge{ListOpenPRsFn: func(context.Context, string, string) ([]forge.PR, error) { return []forge.PR{queued, failed}, nil }, GetPRFn: func(context.Context, string, string, int64) (*forge.PR, error) { return &queued, nil }}
	f.GetCheckStatesFn = func(_ context.Context, _, _, sha string) (map[string]forge.Check, error) {
		if sha == "merge-sha" {
			mergeObserved = true
			return map[string]forge.Check{"ci": {State: pg.CheckStatePending}}, nil
		}
		if sha == "failed-head" && !mergeObserved {
			t.Fatal("diagnosis preceded merge-branch check processing")
		}
		return map[string]forge.Check{"ci": {State: pg.CheckStateFailure}}, nil
	}
	testutil.EnqueueTesting(t, svc, id, 42, "head42", "merge-sha")
	r, err := poller.PollOnce(ctx, &poller.Deps{Forge: f, Queue: svc, RepoID: id, Owner: "org", Repo: "app", ChecksFromConfig: true, PRHeadDiagnostics: true})
	if err != nil || len(r.Errors) > 0 || !mergeObserved {
		t.Fatalf("poll=%+v err=%v mergeObserved=%v", r, err, mergeObserved)
	}
}

func TestOpenHeadsDoNotDisableIdleGating(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		svc, ctx, id := testutil.TestQueueService(t)
		f := &forge.MockForge{ListOpenPRsFn: func(context.Context, string, string) ([]forge.PR, error) {
			return []forge.PR{{Number: 1, HeadSHA: "head", BaseBranch: "main"}}, nil
		}}
		d := &poller.Deps{Forge: f, Queue: svc, RepoID: id, Owner: "org", Repo: "app", IdleGating: true, ChecksFromConfig: true, PRHeadDiagnostics: enabled, FailureDetails: failure.Options{Enabled: true}}
		runPollerTicks(ctx, d, 5)
		if n := len(f.CallsTo("ListOpenPRs")); n != 1 {
			t.Fatalf("diagnostics=%v idle list reads=%d", enabled, n)
		}
	}
}

func TestPublicFailureGroupsIsolateHeadsWithoutEvidence(t *testing.T) {
	for _, description := range []string{"", "Build is failing", "AssertionError: expected 2 got 1"} {
		svc, ctx, id := testutil.TestQueueService(t)
		f := &forge.MockForge{ListOpenPRsFn: func(context.Context, string, string) ([]forge.PR, error) {
			return []forge.PR{{Number: 1, HeadSHA: "first", BaseBranch: "main"}, {Number: 2, HeadSHA: "second", BaseBranch: "main"}}, nil
		}, GetCheckStatesFn: func(context.Context, string, string, string) (map[string]forge.Check, error) {
			return map[string]forge.Check{"ci": {State: pg.CheckStateFailure, Description: description}}, nil
		}}
		r, err := poller.PollOnce(ctx, &poller.Deps{Forge: f, Queue: svc, RepoID: id, Owner: "org", Repo: "app", ChecksFromConfig: true, PRHeadDiagnostics: true})
		if err != nil || len(r.Errors) > 0 {
			t.Fatalf("%+v %v", r, err)
		}
		w := httptest.NewRecorder()
		web.HeadFailuresHandler(svc, func(string) (int64, bool) { return id, true }, true).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/head-failures", nil))
		var body struct {
			Groups []struct {
				Fingerprint string `json:"fingerprint"`
				Count       int    `json:"count"`
			} `json:"groups"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("API %d %s %v", w.Code, w.Body.String(), err)
		}
		if strings.HasPrefix(description, "AssertionError") {
			if len(body.Groups) != 1 || body.Groups[0].Count != 2 {
				t.Fatalf("affirmative evidence did not group: %+v", body)
			}
		} else if len(body.Groups) != 2 || body.Groups[0].Fingerprint == body.Groups[1].Fingerprint || body.Groups[0].Count != 1 || body.Groups[1].Count != 1 {
			t.Fatalf("evidence-free heads grouped: %+v", body)
		}
	}
}

func TestDiagnosisRotationResumesAfterDeadline(t *testing.T) {
	svc, ctx, id := testutil.TestQueueService(t)
	prs := []forge.PR{{Number: 1, HeadSHA: "first", BaseBranch: "main"}, {Number: 2, HeadSHA: "second", BaseBranch: "main"}, {Number: 3, HeadSHA: "third", BaseBranch: "main"}}
	var attempted []string
	slow := true
	f := &forge.MockForge{ListOpenPRsFn: func(context.Context, string, string) ([]forge.PR, error) { return prs, nil }, GetCheckStatesFn: func(ctx context.Context, _, _, head string) (map[string]forge.Check, error) {
		attempted = append(attempted, head)
		if slow {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return map[string]forge.Check{}, nil
	}}
	d := &poller.Deps{Forge: f, Queue: svc, RepoID: id, Owner: "org", Repo: "app", ChecksFromConfig: true, PRHeadDiagnostics: true}
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	r, err := poller.PollOnce(deadline, d)
	if err != nil || len(r.Errors) == 0 || len(attempted) != 1 || attempted[0] != "first" {
		t.Fatalf("deadline poll=%+v err=%v attempted=%v", r, err, attempted)
	}
	slow = false
	r, err = poller.PollOnce(ctx, d)
	if err != nil || len(r.Errors) > 0 || len(attempted) < 2 || attempted[1] != "second" {
		t.Fatalf("rotation restarted/starved tail: %+v %v %v", r, err, attempted)
	}
}
