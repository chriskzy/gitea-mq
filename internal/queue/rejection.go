package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mic92/gitea-mq/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RejectHead persists rejection and claims notification for this queue attempt.
// A repeated or stale handler must finish only its old entry, without notifying.
func (s *Service) RejectHead(ctx context.Context, entry *pg.QueueEntry) (bool, error) {
	claimed := false
	err := s.withTx(ctx, func(q *pg.Queries) error {
		if err := q.LockAdmission(ctx, fmt.Sprintf("%d:%d", entry.RepoID, entry.PrNumber)); err != nil {
			return err
		}
		_, err := q.RejectHead(ctx, entry.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		claimed = err == nil
		return err
	})
	return claimed, err
}

// DequeueEntry cannot remove a replacement attempt for the same PR.
func (s *Service) DequeueEntry(ctx context.Context, id int64) error {
	return s.queries().DeleteEntriesByIDs(ctx, []int64{id})
}

// ObserveClearedIntent records that the forge has actually cleared both explicit
// merge signals. An old approval alone never releases a rejected head.
func (s *Service) ObserveClearedIntent(ctx context.Context, repoID, number int64, head string, observedAt pgtype.Timestamptz) error {
	return s.queries().ObserveClearedIntent(ctx, pg.ObserveClearedIntentParams{RepoID: repoID, PrNumber: number, HeadSha: head, ObservedAt: observedAt})
}

// AdmissionObservationTime binds a forge snapshot to the database clock, so a
// poll begun before rejection cannot release it with stale cancellation data.
func (s *Service) AdmissionObservationTime(ctx context.Context) (pgtype.Timestamptz, error) {
	return s.queries().AdmissionObservationTime(ctx)
}

// EnqueueAutomatic returns nil for a rejected head until a new explicit merge
// signal follows an observed cancellation. Changed heads qualify independently.
func (s *Service) EnqueueAutomatic(ctx context.Context, repoID, number int64, head, target string, priority, explicit bool) (*EnqueueResult, error) {
	var result *EnqueueResult
	err := s.withTx(ctx, func(q *pg.Queries) error {
		if err := q.LockAdmission(ctx, fmt.Sprintf("%d:%d", repoID, number)); err != nil {
			return err
		}
		cleared, err := q.GetRejectedHead(ctx, pg.GetRejectedHeadParams{RepoID: repoID, PrNumber: number, HeadSha: head, TargetBranch: target})
		if err == nil {
			if !explicit || !cleared {
				return nil
			}
			if err = q.ClearRejectedHead(ctx, pg.ClearRejectedHeadParams{RepoID: repoID, PrNumber: number, HeadSha: head, TargetBranch: target}); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result, err = s.enqueue(ctx, q, repoID, number, head, target, priority)
		return err
	})
	return result, err
}
