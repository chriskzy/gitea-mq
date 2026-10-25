package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mic92/gitea-mq/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RejectHead fences forge cleanup against duplicate handlers and replacement
// admission. The rejection claim commits before action; an interrupted action
// can finish after restart without repeating its notification. action must not
// use the queue pool: this method owns the connection for the whole operation.
// Batches retain their entry until SaveBatch, after forge cleanup is complete.
func (s *Service) RejectHead(ctx context.Context, entry *pg.QueueEntry, remove bool, action func(notify bool)) (claimed bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	q := pg.New(conn)
	key := fmt.Sprintf("%d:%d", entry.RepoID, entry.PrNumber)
	if err = q.LockRejection(ctx, key); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Conn().Close(cleanup)
		return false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		unlocked, unlockErr := q.UnlockRejection(cleanup, key)
		if unlockErr != nil || !unlocked {
			_ = conn.Conn().Close(cleanup)
			if err == nil {
				err = fmt.Errorf("release rejection lock: unlocked=%v error=%v", unlocked, unlockErr)
			}
		}
	}()
	current, err := q.GetQueueEntry(ctx, pg.GetQueueEntryParams{RepoID: entry.RepoID, PrNumber: entry.PrNumber})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.ID != entry.ID {
		return false, nil
	}
	_, err = q.RejectHead(ctx, entry.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	claimed = err == nil
	if action != nil {
		action(claimed)
	}
	if remove {
		err = q.DeleteEntriesByIDs(ctx, []int64{entry.ID})
	} else {
		err = nil
	}
	return claimed, err
}

// ObserveClearedIntent records that the forge has actually cleared both explicit
// merge signals. An old approval alone never releases a rejected head.
func (s *Service) ObserveClearedIntent(ctx context.Context, repoID, number int64, head string, observedAt pgtype.Timestamptz) error {
	return s.queries().ObserveClearedIntent(ctx, pg.ObserveClearedIntentParams{RepoID: repoID, PrNumber: number, HeadSha: head, ObservedAt: observedAt})
}

// DequeueEntry cannot remove a replacement attempt for the same PR.
func (s *Service) DequeueEntry(ctx context.Context, id int64) error {
	return s.queries().DeleteEntriesByIDs(ctx, []int64{id})
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

// IsRejectedAttempt identifies interrupted cleanup by immutable entry ID.
func (s *Service) IsRejectedAttempt(ctx context.Context, entryID int64) (bool, error) {
	return s.queries().IsRejectedAttempt(ctx, entryID)
}
