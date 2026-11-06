package poller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/Mic92/gitea-mq/internal/failure"
	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/monitor"
	"github.com/Mic92/gitea-mq/internal/queue"
)

// These observations never enqueue, retry, comment, or set a commit status.
// At most ten PR head reads and thirty log reads are added per reconcile.
func observeHeadFailures(ctx context.Context, deps *Deps, result *PollResult, prs []forge.PR) {
	if !deps.PRHeadDiagnostics {
		return
	}
	// Advisory I/O runs after queue work and cannot monopolize the next tick.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	admitted := make([]forge.PR, 0, len(prs))
	for _, pr := range prs {
		if !pr.Draft && (deps.PRChecks == nil || len(*deps.PRChecks) > 0) && (len(deps.TargetBranches) == 0 || slices.Contains(deps.TargetBranches, pr.BaseBranch)) {
			admitted = append(admitted, pr)
		}
	}
	deps.DiagnosisHeads.Update(admitted)
	if err := deps.Queue.PruneHeadDiagnoses(ctx, deps.RepoID, admitted); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("invalidate PR head diagnoses: %w", err))
		return
	}
	prs = admitted
	old, err := deps.Queue.HeadDiagnoses(ctx, deps.RepoID)
	if err != nil {
		result.Errors = append(result.Errors, err)
		return
	}
	byPR := make(map[int64]queue.HeadDiagnosis, len(old))
	for _, d := range old {
		byPR[d.PR] = d
	}
	if len(prs) == 0 {
		deps.diagnosisOffset = 0
		return
	}
	prs = slices.Clone(prs)
	slices.SortFunc(prs, func(a, b forge.PR) int {
		if a.Number < b.Number {
			return -1
		}
		if a.Number > b.Number {
			return 1
		}
		return 0
	})
	n := min(len(prs), 10)
	start := deps.diagnosisOffset % len(prs)
	deps.diagnosisOffset = (start + n) % len(prs)
	for i := range n {
		if ctx.Err() != nil {
			result.Errors = append(result.Errors, ctx.Err())
			return
		}
		pr := prs[(start+i)%len(prs)]
		if deps.PRChecks != nil && len(*deps.PRChecks) == 0 {
			_ = deps.Queue.ClearHeadDiagnosis(ctx, deps.RepoID, pr.Number, pr.HeadSHA)
			continue
		}
		required, err := monitor.ResolveCheckList(ctx, deps.Forge, deps.Owner, deps.Repo, pr.BaseBranch, deps.PRChecks, deps.FallbackChecks, deps.ChecksFromConfig)
		if err != nil {
			_ = deps.Queue.MarkHeadDiagnosisUnavailable(ctx, deps.RepoID, pr.Number, pr.HeadSHA)
			result.Errors = append(result.Errors, err)
			continue
		}
		states, err := deps.Forge.GetCheckStates(ctx, deps.Owner, deps.Repo, pr.HeadSHA)
		if err != nil {
			_ = deps.Queue.MarkHeadDiagnosisUnavailable(ctx, deps.RepoID, pr.Number, pr.HeadSHA)
			result.Errors = append(result.Errors, err)
			continue
		}
		opts := deps.FailureDetails
		opts.Enabled = true
		opts.LogLines = 0
		checks := failure.CollectDiagnosticStates(ctx, opts, deps.Forge, deps.Owner, deps.Repo, states, required)
		if len(checks) == 0 {
			if err := deps.Queue.ClearHeadDiagnosis(ctx, deps.RepoID, pr.Number, pr.HeadSHA); err != nil {
				result.Errors = append(result.Errors, err)
			}
			continue
		}
		raw, _ := json.Marshal(checks)
		sum := sha256.Sum256(raw)
		signature := hex.EncodeToString(sum[:])
		if d := byPR[pr.Number]; d.HeadSHA == pr.HeadSHA && d.Signature == signature && d.ObservationError == "" && deps.now().Sub(d.ObservedAt) < 15*time.Minute {
			continue
		}
		opts.LogLines = deps.FailureDetails.LogLines
		checks = failure.CollectDiagnosticStates(ctx, opts, deps.Forge, deps.Owner, deps.Repo, states, required)
		d := queue.HeadDiagnosis{PR: pr.Number, HeadSHA: pr.HeadSHA, TargetBranch: pr.BaseBranch, Signature: signature, Failures: failure.Diagnose(checks, deps.Retry.Patterns)}
		changed, err := deps.Queue.SaveHeadDiagnosis(ctx, deps.RepoID, d)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		if changed {
			deps.Queue.RecordEvent(ctx, queue.Event{RepoID: deps.RepoID, Kind: queue.EventPRHeadFailed, PRNumber: pr.Number, HeadSHA: pr.HeadSHA, TargetBranch: pr.BaseBranch, Reason: "pr_head_ci", Detail: map[string]any{"failures": d.Failures}})
		}
	}
}
