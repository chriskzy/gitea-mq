package forge

import (
	"context"
	"fmt"
	"log/slog"
)

// ApprovalRequirer is optionally implemented by forges whose branch
// protection states how many approvals a merge needs (Gitea). Reading it
// may need repo admin rights.
type ApprovalRequirer interface {
	RequiredApprovals(ctx context.Context, owner, name, branch string) (int, error)
}

// ApprovalPolicy decides whether a PR has the approval the queue requires
// (GITEA_MQ_REQUIRE_APPROVAL / GITEA_MQ_REQUIRED_APPROVALS /
// GITEA_MQ_ENQUEUE_ON_APPROVAL). The zero value requires nothing.
type ApprovalPolicy struct {
	// Require holds PRs back until approved.
	Require bool
	// Count is the number of approvals needed. 0 reads it from branch
	// protection when the forge exposes it (and FromConfig is unset), with
	// 1 as the floor.
	Count int
	// OnApproval treats a qualifying approval as merge intent.
	OnApproval bool
	// FromConfig forbids reading branch protection (non-admin tokens).
	FromConfig bool
	// DistinctReviewers is a floor on approvals from distinct reviewer
	// accounts (each account counts once, at its latest verdict).
	DistinctReviewers int
}

// Needed is the number of approvals required for branch.
func (p ApprovalPolicy) Needed(ctx context.Context, f Forge, owner, name, branch string) int {
	return max(p.needed(ctx, f, owner, name, branch), p.DistinctReviewers)
}

func (p ApprovalPolicy) needed(ctx context.Context, f Forge, owner, name, branch string) int {
	if p.Count > 0 {
		return p.Count
	}
	if ar, ok := f.(ApprovalRequirer); ok && !p.FromConfig {
		n, err := ar.RequiredApprovals(ctx, owner, name, branch)
		if err != nil {
			slog.Debug("could not read required approvals from protection, assuming 1",
				"owner", owner, "repo", name, "branch", branch, "error", err)
		} else if n > 0 {
			return n
		}
	}
	return 1
}

// Check returns the PR's reviews at head and how many approvals it needs. ok
// is true when approval is not required or the PR has it.
func (p ApprovalPolicy) Check(ctx context.Context, f Forge, owner, name, branch string, number int64, head string) (ok bool, rv Reviews, needed int, err error) {
	if !p.Require {
		return true, Reviews{}, 0, nil
	}
	needed = p.Needed(ctx, f, owner, name, branch)
	rv, err = GetReviews(ctx, f, owner, name, number, head)
	if err != nil {
		return false, rv, needed, err
	}
	return rv.Approved(needed), rv, needed, nil
}

// WaitingStatus is the MQ status description of a PR held back by the
// approval gate.
func WaitingStatus(rv Reviews, needed int) string {
	switch {
	case len(rv.Conflicts) > 0:
		return ConflictStatus(rv.Conflicts)
	case rv.ChangesRequested:
		return "Changes requested: waiting for approval"
	default:
		return fmt.Sprintf("Waiting for approval (%d/%d)", rv.Approvals, needed)
	}
}
