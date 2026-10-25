package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mic92/gitea-mq/internal/store/pg"
	"github.com/jackc/pgx/v5"
)

// RejectHead persists rejection before the entry is removed or the forge is notified.
func (s *Service) RejectHead(ctx context.Context, entry *pg.QueueEntry) error {
	return s.withTx(ctx, func(q *pg.Queries) error {
		if err := q.LockAdmission(ctx, fmt.Sprintf("%d:%d", entry.RepoID, entry.PrNumber)); err != nil {
			return err
		}
		return q.RejectHead(ctx, pg.RejectHeadParams{RepoID: entry.RepoID, PrNumber: entry.PrNumber, HeadSha: entry.PrHeadSha, TargetBranch: entry.TargetBranch})
	})
}

// ObserveClearedIntent records that the forge has actually cleared both explicit
// merge signals. An old approval alone never releases a rejected head.
func (s *Service) ObserveClearedIntent(ctx context.Context, repoID, number int64, head string) error {
	return s.queries().ObserveClearedIntent(ctx, pg.ObserveClearedIntentParams{RepoID: repoID, PrNumber: number, HeadSha: head})
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
