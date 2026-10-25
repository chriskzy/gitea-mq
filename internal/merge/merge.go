// Package merge handles merge branch creation, cleanup, and startup
// reconciliation for the merge queue.
package merge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/logutil"
	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/store/pg"
)

// BranchPrefix is the prefix for temporary merge branches created by gitea-mq.
const BranchPrefix = "gitea-mq/"

// StaleMirrorDescription marks gitea-mq/* mirrored statuses that were reset
// when a PR re-entered testing. The forge has no delete-status API, so we
// overwrite with this sentinel and later flip matching pendings to skipped.
const StaleMirrorDescription = "From a previous merge queue attempt"

func BranchName(prNumber int64) string {
	return fmt.Sprintf("%s%d", BranchPrefix, prNumber)
}

type StartTestingResult struct {
	MergeBranchName string
	MergeBranchSHA  string
	Removed         bool // true if the PR was removed from the queue instead of entering testing
}

// StartTesting creates a merge branch for the head-of-queue PR and
// transitions it to the "testing" state. If the merge conflicts, the PR
// is removed from the queue with automerge cancelled and a comment posted.
func StartTesting(ctx context.Context, f forge.Forge, svc *queue.Service, owner, repo string, repoID int64, entry *pg.QueueEntry, externalURL, mergeLabel string) (*StartTestingResult, error) {
	if recovered, err := RecoverRejected(ctx, f, svc, owner, repo, entry, externalURL, mergeLabel); err != nil || recovered {
		return &StartTestingResult{Removed: recovered}, err
	}
	branchName := BranchName(entry.PrNumber)
	targetURL := forge.DashboardPRURL(externalURL, f.Kind(), owner, repo, entry.PrNumber)

	mergeSHA, conflict, err := f.CreateMergeBranch(ctx, owner, repo, entry.TargetBranch, entry.PrHeadSha, branchName)
	if conflict || err != nil {
		reason, desc, state := "merge_conflict", "Merge conflict with target branch", pg.CheckStateFailure
		comment := "❌ Removed from merge queue: merge conflict with target branch. Please rebase and re-schedule the merge."
		var detail map[string]any
		if err != nil && !conflict {
			reason, desc, state = "merge_error", "Failed to create merge branch", pg.CheckStateError
			comment = fmt.Sprintf("❌ Removed from merge queue: failed to create merge branch.\n\n```\n%v\n```", err)
			detail = map[string]any{"error": err.Error()}
		}
		claimed, rejectErr := svc.RejectHead(ctx, entry, true, func(notify bool) {
			logutil.WarnIfErr(forge.CancelMergeIntent(ctx, f, owner, repo, entry.PrNumber, mergeLabel), "cancel merge intent failed", "pr", entry.PrNumber)
			logutil.WarnIfErr(f.SetMQStatus(ctx, owner, repo, entry.PrHeadSha, forge.MQStatus{State: state, Description: desc, TargetURL: targetURL}), "set mq status failed", "pr", entry.PrNumber)
			if notify {
				logutil.WarnIfErr(f.Comment(ctx, owner, repo, entry.PrNumber, comment), "post comment failed", "pr", entry.PrNumber)
			}
			SkipPendingMirroredChecks(ctx, f, owner, repo, entry.PrHeadSha)
		})
		if rejectErr != nil {
			return nil, fmt.Errorf("reject queue attempt: %w", rejectErr)
		}
		if claimed {
			svc.RecordEvent(ctx, ejectEvent(repoID, entry, reason, detail))
		}
		return &StartTestingResult{Removed: true}, nil
	}

	if err := svc.SetMergeBranch(ctx, repoID, entry.PrNumber, branchName, mergeSHA); err != nil {
		return nil, fmt.Errorf("set merge branch for PR #%d: %w", entry.PrNumber, err)
	}
	if err := svc.UpdateState(ctx, repoID, entry.PrNumber, pg.EntryStateTesting); err != nil {
		return nil, fmt.Errorf("update state to testing for PR #%d: %w", entry.PrNumber, err)
	}

	// Reset stale gitea-mq/* mirrors from a previous attempt so they don't
	// show outdated results while new CI runs.
	clearStaleMirroredStatuses(ctx, f, owner, repo, entry.PrHeadSha)

	logutil.WarnIfErr(f.SetMQStatus(ctx, owner, repo, entry.PrHeadSha, forge.MQStatus{
		State: pg.CheckStatePending, Description: "Testing merge result", TargetURL: targetURL,
	}), "set mq status failed", "pr", entry.PrNumber)

	svc.RecordEvent(ctx, queue.Event{
		RepoID: repoID, Kind: queue.EventTestingStarted, PRNumber: entry.PrNumber,
		TargetBranch: entry.TargetBranch, HeadSHA: entry.PrHeadSha,
		Detail: map[string]any{"branch": branchName, "sha": mergeSHA},
	})
	slog.Info("started testing", "pr", entry.PrNumber, "branch", branchName, "sha", mergeSHA)

	return &StartTestingResult{MergeBranchName: branchName, MergeBranchSHA: mergeSHA}, nil
}

// ejectEvent is the queue event for gitea-mq removing entry for reason.
func ejectEvent(repoID int64, entry *pg.QueueEntry, reason string, detail map[string]any) queue.Event {
	return queue.Event{
		RepoID: repoID, Kind: queue.EventEjected, PRNumber: entry.PrNumber,
		TargetBranch: entry.TargetBranch, HeadSHA: entry.PrHeadSha, Reason: reason, Detail: detail,
		EnqueuedAt: entry.EnqueuedAt.Time,
	}
}

// SkipPendingMirroredChecks closes mirrored checks after the queue stops
// waiting for their merge-branch results.
func SkipPendingMirroredChecks(ctx context.Context, f forge.Forge, owner, repo, sha string) {
	checks, err := f.GetCheckStates(ctx, owner, repo, sha)
	if err != nil {
		slog.Warn("failed to fetch commit statuses for skip cleanup", "sha", sha, "error", err)
		return
	}
	for ctxName, check := range checks {
		if !strings.HasPrefix(ctxName, forge.MirrorContextPrefix) || check.State != pg.CheckStatePending {
			continue
		}
		logutil.WarnIfErr(f.MirrorCheck(ctx, owner, repo, sha, ctxName, forge.Check{
			State:       forge.CheckState("skipped"),
			Description: "Merge queue stopped waiting for this check",
			TargetURL:   check.TargetURL,
		}), "mirror check skip failed", "sha", sha, "context", ctxName)
	}
}

func clearStaleMirroredStatuses(ctx context.Context, f forge.Forge, owner, repo, sha string) {
	checks, err := f.GetCheckStates(ctx, owner, repo, sha)
	if err != nil {
		slog.Warn("failed to fetch commit statuses for stale cleanup", "sha", sha, "error", err)
		return
	}
	for ctxName := range checks {
		if !forge.IsOwnContext(ctxName) {
			continue
		}
		logutil.WarnIfErr(f.MirrorCheck(ctx, owner, repo, sha, ctxName, forge.Check{
			State:       pg.CheckStatePending,
			Description: StaleMirrorDescription,
		}), "mirror check reset failed", "sha", sha, "context", ctxName)
	}
}

func CleanupMergeBranch(ctx context.Context, f forge.Forge, owner, repo string, entry *pg.QueueEntry) {
	if !entry.MergeBranchName.Valid || entry.MergeBranchName.String == "" {
		return
	}
	if err := f.DeleteBranch(ctx, owner, repo, entry.MergeBranchName.String); err != nil {
		slog.Warn("failed to delete merge branch", "branch", entry.MergeBranchName.String, "error", err)
	}
}

// CleanupStaleBranches deletes orphaned gitea-mq/* branches not referenced by
// any active queue entry or live batch. Called on startup to clean up after
// crashes. spare lists additional branch names to keep (live batch branches).
func CleanupStaleBranches(ctx context.Context, f forge.Forge, svc *queue.Service, owner, repo string, repoID int64, spare []string) error {
	activeEntries, err := svc.ListActiveEntries(ctx, repoID)
	if err != nil {
		return fmt.Errorf("list active entries: %w", err)
	}

	activeBranches := make(map[string]bool, len(activeEntries)+len(spare))
	for _, e := range activeEntries {
		if e.MergeBranchName.Valid && e.MergeBranchName.String != "" {
			activeBranches[e.MergeBranchName.String] = true
		}
	}
	for _, b := range spare {
		activeBranches[b] = true
	}

	branches, err := f.ListBranches(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}

	var deleted int
	for _, b := range branches {
		if !strings.HasPrefix(b, BranchPrefix) {
			continue
		}
		if activeBranches[b] {
			continue
		}

		slog.Info("deleting stale merge branch", "owner", owner, "repo", repo, "branch", b)
		if err := f.DeleteBranch(ctx, owner, repo, b); err != nil {
			slog.Warn("failed to delete stale branch", "branch", b, "error", err)
			continue
		}
		deleted++
	}

	slog.Info("startup merge branch cleanup", "owner", owner, "repo", repo,
		"active_branches", len(activeBranches), "stale_deleted", deleted)
	return nil
}

// RecoverRejected finishes an interrupted terminal removal before testing or
// success can resume. The persisted claim prevents a second notification.
func RecoverRejected(ctx context.Context, f forge.Forge, svc *queue.Service, owner, repo string, entry *pg.QueueEntry, externalURL, mergeLabel string) (bool, error) {
	rejected, err := svc.IsRejectedAttempt(ctx, entry.ID)
	if err != nil || !rejected {
		return false, err
	}
	_, err = svc.RejectHead(ctx, entry, true, func(bool) {
		logutil.WarnIfErr(f.SetMQStatus(ctx, owner, repo, entry.PrHeadSha, forge.MQStatus{State: pg.CheckStateFailure, Description: "Previous queue attempt was rejected", TargetURL: forge.DashboardPRURL(externalURL, f.Kind(), owner, repo, entry.PrNumber)}), "restore rejected status failed", "pr", entry.PrNumber)
		logutil.WarnIfErr(forge.CancelMergeIntent(ctx, f, owner, repo, entry.PrNumber, mergeLabel), "cancel rejected intent failed", "pr", entry.PrNumber)
		CleanupMergeBranch(ctx, f, owner, repo, entry)
		SkipPendingMirroredChecks(ctx, f, owner, repo, entry.PrHeadSha)
	})
	return true, err
}
