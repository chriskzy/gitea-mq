package poller_test

import (
	"context"
	"testing"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/gitea"
	"github.com/Mic92/gitea-mq/internal/poller"
)

func TestTargetBranchAdmissionBeforeCI(t *testing.T) {
	for _, source := range []string{"approval", "automerge", "label"} {
		t.Run(source, func(t *testing.T) {
			deps, mock, svc, ctx, repoID := setupPollerTest(t)
			deps.TargetBranches = []string{"main"}
			deps.Approval = forge.ApprovalPolicy{Require: true, Count: 1, OnApproval: true}
			deps.MergeLabel = "merge-queue"
			pr := makePR(42, "sha42", "feature-parent")
			if source == "label" {
				pr.Labels = []*gitea.Label{{Name: "merge-queue"}}
			}
			mock.ListOpenPRsFn = func(context.Context, string, string) ([]gitea.PR, error) { return []gitea.PR{pr}, nil }
			mock.GetPRFn = func(context.Context, string, string, int64) (*gitea.PR, error) { return &pr, nil }
			mock.GetPRTimelineFn = func(context.Context, string, string, int64) ([]gitea.TimelineComment, error) {
				if source == "automerge" {
					return automergeTimeline(), nil
				}
				return nil, nil
			}
			mock.ListPRReviewsFn = approvedBy("alice")
			for range 3 {
				got, err := poller.PollOnce(ctx, deps)
				if err != nil || len(got.Errors) != 0 || len(got.Enqueued) != 0 {
					t.Fatalf("ineligible base admitted: %+v %v", got, err)
				}
			}
			if entry, err := svc.GetEntry(ctx, repoID, 42); err != nil || entry != nil {
				t.Fatalf("unexpected queue entry: %+v %v", entry, err)
			}
			if len(mock.CallsTo("MergeBranches")) != 0 || len(mock.CallsTo("GetCombinedCommitStatus")) != 0 {
				t.Fatal("ineligible base consumed CI or merge work")
			}
			if len(mock.CallsTo("CreateComment")) != 0 {
				t.Fatal("waiting target flooded comments")
			}
			if calls := mock.CallsTo("CreateCommitStatus"); len(calls) != 1 {
				t.Fatalf("waiting status not deduplicated across three polls: %d writes", len(calls))
			}
			// Retargeting is enough; no fabricated source push or approval transfer.
			pr.Base.Ref = "main"
			got, err := poller.PollOnce(ctx, deps)
			if err != nil || len(got.Errors) != 0 || len(got.Enqueued) != 1 {
				t.Fatalf("eligible retarget not admitted: %+v %v", got, err)
			}
		})
	}
}

func TestTargetBranchAdmissionChecksRawAndResolvedTargets(t *testing.T) {
	for _, targets := range []struct{ base, resolved string }{
		{base: "main", resolved: "release/stable"},
		{base: "feature-parent", resolved: "main"},
	} {
		t.Run(targets.base+"_to_"+targets.resolved, func(t *testing.T) {
			deps, mock, svc, ctx, repoID := setupLabelTest(t)
			deps.TargetBranches = []string{"main"}
			mock.ListOpenPRsFn = func(context.Context, string, string) ([]forge.PR, error) {
				return []forge.PR{{Number: 42, State: "open", HeadSHA: "sha42", BaseBranch: targets.base,
					Labels: []string{"merge-queue"}}}, nil
			}
			mock.ResolveStackFn = func(context.Context, string, string, int64) (*forge.Stack, error) {
				return &forge.Stack{BaseBranch: targets.resolved, PRs: []forge.StackPR{{Number: 42, HeadSHA: "sha42"}}}, nil
			}
			for range 3 {
				got, err := poller.PollOnce(ctx, deps)
				if err != nil || len(got.Errors) != 0 || len(got.Enqueued) != 0 {
					t.Fatalf("ineligible target pair admitted: %+v %v", got, err)
				}
			}
			if entry, err := svc.GetEntry(ctx, repoID, 42); err != nil || entry != nil {
				t.Fatalf("unexpected queue entry: %+v %v", entry, err)
			}
			for _, operation := range []string{"GetCheckStates", "CreateMergeBranch", "MergePR"} {
				if len(mock.CallsTo(operation)) != 0 {
					t.Fatalf("ineligible target consumed %s", operation)
				}
			}
			if calls := mock.CallsTo("SetMQStatus"); len(calls) != 1 {
				t.Fatalf("waiting status not deduplicated across three polls: %d writes", len(calls))
			}
		})
	}
}

func TestTargetBranchAdmissionDefaultAllowsNonMain(t *testing.T) {
	for _, setting := range []struct {
		name     string
		branches []string
	}{
		{name: "nil"},
		{name: "empty", branches: []string{}},
	} {
		t.Run(setting.name, func(t *testing.T) {
			deps, mock, svc, ctx, repoID := setupLabelTest(t)
			deps.TargetBranches = setting.branches
			mock.ListOpenPRsFn = func(context.Context, string, string) ([]forge.PR, error) {
				return []forge.PR{{Number: 42, State: "open", HeadSHA: "sha42", BaseBranch: "release/stable",
					Labels: []string{"merge-queue"}}}, nil
			}
			mock.CreateMergeBranchFn = func(context.Context, string, string, string, string, string) (string, bool, error) {
				return "merge42", false, nil
			}
			got, err := poller.PollOnce(ctx, deps)
			if err != nil || len(got.Errors) != 0 || len(got.Enqueued) != 1 {
				t.Fatalf("default blocked non-main target: %+v %v", got, err)
			}
			entry, err := svc.GetEntry(ctx, repoID, 42)
			if err != nil || entry == nil || entry.TargetBranch != "release/stable" {
				t.Fatalf("non-main queue target lost: %+v %v", entry, err)
			}
		})
	}
}
