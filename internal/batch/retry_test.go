package batch_test

import (
	"context"
	"testing"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/retry"
	"github.com/Mic92/gitea-mq/internal/store/pg"
)

// rerunForge adds Actions log and rerun support to the batch fake.
type rerunForge struct {
	*forge.MockForge
	log    string
	reruns int
}

func (r *rerunForge) JobLog(context.Context, string, string, string) (string, []byte, error) {
	return "build (push)", []byte(r.log), nil
}

func (r *rerunForge) RerunJob(context.Context, string, string, string) error {
	r.reruns++
	return nil
}

func TestHandleFail_InfraRetryMarksFlakyAndLands(t *testing.T) {
	e, f, svc, ctx := setup(t, 10, 20)
	f.merged[10], f.merged[20] = true, true
	rf := &rerunForge{MockForge: f.MockForge, log: "dial unix /run/engine.sock: no such file or directory"}
	e.Forge = rf
	patterns, _ := retry.ParsePatterns(`engine\.sock: no such file`)
	e.Retry = retry.Policy{Patterns: patterns, Max: 1, Tracker: retry.NewTracker()}
	checks := []string{"ci"}
	e.MergeChecks = &checks
	f.GetCheckStatesFn = func(context.Context, string, string, string) (map[string]forge.Check, error) {
		return map[string]forge.Check{"ci": {State: pg.CheckStateFailure, TargetURL: "http://ci/actions/runs/1/jobs/0"}}, nil
	}

	b, err := e.FormAndBuild(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.HandleFail(ctx, b, "ci", ""); err != nil {
		t.Fatal(err)
	}
	if rf.reruns != 1 || len(b.CurrentIds) != 2 {
		t.Fatalf("want one rerun and no bisect: reruns=%d current=%v", rf.reruns, b.CurrentIds)
	}
	evs, _ := svc.ListEvents(ctx, queue.EventFilter{RepoID: e.RepoID, Kind: queue.EventInfraRetry, Limit: 10})
	if len(evs) != 1 {
		t.Fatalf("infra_retry events = %+v", evs)
	}

	// The rerun passes: the whole batch lands and is marked flaky.
	if err := e.HandlePass(ctx, b); err != nil {
		t.Fatal(err)
	}
	if !b.Flaky || b.State != pg.BatchStateDone || len(b.LandedIds) != 2 || b.Builds != 1 {
		t.Fatalf("want flaky done, 2 landed, 1 build: %+v", b)
	}
}

func TestHandleFail_InfraRetrySecondFailureBisects(t *testing.T) {
	e, f, _, ctx := setup(t, 10, 20)
	rf := &rerunForge{MockForge: f.MockForge, log: "engine.sock: no such file"}
	e.Forge = rf
	patterns, _ := retry.ParsePatterns(`engine\.sock`)
	e.Retry = retry.Policy{Patterns: patterns, Max: 1, Tracker: retry.NewTracker()}
	f.GetCheckStatesFn = func(context.Context, string, string, string) (map[string]forge.Check, error) {
		return map[string]forge.Check{"ci": {State: pg.CheckStateFailure, TargetURL: "http://ci/actions/runs/1/jobs/0"}}, nil
	}
	checks := []string{"ci"}
	e.MergeChecks = &checks

	b, _ := e.FormAndBuild(ctx, "main")
	_ = e.HandleFail(ctx, b, "ci", "")
	_ = e.HandleFail(ctx, b, "ci", "")
	if rf.reruns != 1 || len(b.CurrentIds) != 1 {
		t.Fatalf("second failure should bisect: reruns=%d current=%v", rf.reruns, b.CurrentIds)
	}
}
