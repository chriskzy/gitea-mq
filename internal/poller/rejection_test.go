package poller_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/batch"
	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/gitea"
	"github.com/Mic92/gitea-mq/internal/poller"
	"github.com/Mic92/gitea-mq/internal/testutil"
)

func TestRejectedHeadDoesNotReenterFromUnchangedIntent(t *testing.T) {
	for _, batching := range []bool{false, true} {
		for _, source := range []string{"approval", "automerge", "label"} {
			t.Run(fmt.Sprintf("batch=%v/%s", batching, source), func(t *testing.T) {
				deps, mock, svc, ctx, repoID := setupPollerTest(t)
				deps.Approval = forge.ApprovalPolicy{Require: true, Count: 1, OnApproval: true}
				deps.MergeLabel = "merge-queue"
				pr := makePR(42, "sha42", "main")
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
				mock.MergeBranchesFn = func(context.Context, string, string, string, string, string) (*gitea.MergeResult, error) {
					return nil, &gitea.MergeConflictError{}
				}
				mock.StackMergesFn = func(context.Context, string, string, string, []string, string) (string, []gitea.StackStep, error) {
					return "", []gitea.StackStep{{Conflict: true}}, nil
				}
				if batching {
					deps.Batch = &batch.Engine{Forge: deps.Forge, Queue: svc, RepoID: repoID, Owner: "org", Repo: "app", BatchMax: 4}
				}
				first, err := poller.PollOnce(ctx, deps)
				if err != nil || len(first.Enqueued) != 1 {
					t.Fatalf("first=%+v err=%v", first, err)
				}
				for range 3 {
					next, err := poller.PollOnce(ctx, deps)
					if err != nil || len(next.Errors) > 0 || len(next.Enqueued) != 0 {
						t.Fatalf("repeated rejected head: %+v err=%v", next, err)
					}
				}
				if n := len(mock.CallsTo("CreateComment")); n != 1 {
					t.Fatalf("removal comments=%d, want 1", n)
				}
				if e, _ := svc.GetEntry(ctx, repoID, 42); e != nil {
					t.Fatalf("rejected head still queued: %+v", e)
				}
				// Observing cancellation does not make the old approval new intent.
				source = "approval"
				pr.Labels = nil
				cleared, err := poller.PollOnce(ctx, deps)
				if err != nil || len(cleared.Enqueued) != 0 {
					t.Fatalf("approval retried after cancellation: %+v %v", cleared, err)
				}
				// A subsequent operator reschedule can retry the same source head.
				source = "automerge"
				mergeOK(mock)
				mock.StackMergesFn = func(context.Context, string, string, string, []string, string) (string, []gitea.StackStep, error) {
					return "merge-sha", []gitea.StackStep{{}}, nil
				}
				retry, err := poller.PollOnce(ctx, deps)
				if err != nil || len(retry.Errors) > 0 || len(retry.Enqueued) != 1 {
					t.Fatalf("fresh reschedule not admitted: %+v %v", retry, err)
				}
			})
		}
	}
}

func TestRejectedTimeoutDoesNotReenterFromApproval(t *testing.T) {
	deps, mock, svc, ctx, repoID := setupPollerTest(t)
	deps.Approval = forge.ApprovalPolicy{Require: true, Count: 1, OnApproval: true}
	deps.CheckTimeout = time.Nanosecond
	pr := makePR(42, "sha42", "main")
	mock.ListOpenPRsFn = func(context.Context, string, string) ([]gitea.PR, error) { return []gitea.PR{pr}, nil }
	mock.GetPRFn = func(context.Context, string, string, int64) (*gitea.PR, error) { return &pr, nil }
	mock.ListPRReviewsFn = approvedBy("alice")
	testutil.EnqueueTesting(t, svc, repoID, 42, "sha42", "merge-sha")
	for range 3 {
		result, err := poller.PollOnce(ctx, deps)
		if err != nil || len(result.Errors) > 0 || len(result.Enqueued) > 0 {
			t.Fatalf("rejected timeout retried: %+v %v", result, err)
		}
	}
	if n := len(mock.CallsTo("CreateComment")); n != 1 {
		t.Fatalf("comments=%d, want 1", n)
	}
}
