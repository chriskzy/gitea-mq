package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/Mic92/gitea-mq/internal/store/pg"
	"github.com/jackc/pgx/v5/pgtype"
)

// EventKind names a queue transition recorded in queue_events.
type EventKind string

const (
	EventEnqueued       EventKind = "enqueued"
	EventTestingStarted EventKind = "testing_started"
	EventPassed         EventKind = "passed"
	EventMerged         EventKind = "merged"
	// EventEjected: gitea-mq removed the PR against the author's intent
	// (failed check, conflict, timeout, push, ...). Reason says why.
	EventEjected EventKind = "ejected"
	// EventRemoved: the PR left the queue because the author or forge
	// withdrew it (closed, merge intent cancelled).
	EventRemoved       EventKind = "removed"
	EventBatchFormed   EventKind = "batch_formed"
	EventBatchBisected EventKind = "batch_bisected"
	EventBatchLanded   EventKind = "batch_landed"
	// EventReviewConflict: the approval gate saw conflicting reviews at a
	// PR head. Recorded once per head.
	EventReviewConflict EventKind = "review_conflict"
)

// Event is one queue transition. Zero PRNumber/BatchID are stored as NULL.
type Event struct {
	RepoID       int64
	Kind         EventKind
	PRNumber     int64
	BatchID      int64
	TargetBranch string
	HeadSHA      string
	Reason       string
	Detail       map[string]any
	// EnqueuedAt is set on events that end a PR's time in the queue
	// (merged, ejected, removed). RecordEvent stores the elapsed time as
	// detail.queued_seconds.
	EnqueuedAt time.Time

	// ID and Time are filled by RecordEvent before observers run. ID is 0
	// when the row could not be stored.
	ID   int64
	Time time.Time
}

// QueuedFor is how long the PR was queued, or 0 when unknown.
func (e Event) QueuedFor(now time.Time) time.Duration {
	if e.EnqueuedAt.IsZero() || now.Before(e.EnqueuedAt) {
		return 0
	}
	return now.Sub(e.EnqueuedAt)
}

// EventObserver is told about every recorded event, e.g. to update in-memory
// metrics. repo is "<forge>:<owner>/<name>", or "" if the repo is unknown.
type EventObserver func(repo string, e Event)

type eventState struct {
	mu        sync.RWMutex
	repoNames map[int64]string
	observers []EventObserver
}

// OnEvent registers an observer. Call before the queue is in use.
func (s *Service) OnEvent(o EventObserver) {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	s.events.observers = append(s.events.observers, o)
}

func (s *Service) rememberRepo(r pg.Repo) {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	if s.events.repoNames == nil {
		s.events.repoNames = map[int64]string{}
	}
	s.events.repoNames[r.ID] = r.Forge + ":" + r.Owner + "/" + r.Name
}

// RecordEvent stores e. It is best-effort: the queue must keep moving when
// the event log cannot be written, so errors are only logged.
func (s *Service) RecordEvent(ctx context.Context, e Event) {
	if d := e.QueuedFor(time.Now()); d > 0 {
		if e.Detail == nil {
			e.Detail = map[string]any{}
		}
		e.Detail["queued_seconds"] = int64(d.Seconds())
	}
	detail := []byte("{}")
	if len(e.Detail) > 0 {
		if b, err := json.Marshal(e.Detail); err == nil {
			detail = b
		}
	}
	row, err := s.queries().InsertEvent(context.WithoutCancel(ctx), pg.InsertEventParams{
		RepoID:       e.RepoID,
		Kind:         string(e.Kind),
		PrNumber:     pgtype.Int8{Int64: e.PRNumber, Valid: e.PRNumber != 0},
		BatchID:      pgtype.Int8{Int64: e.BatchID, Valid: e.BatchID != 0},
		TargetBranch: e.TargetBranch,
		HeadSha:      e.HeadSHA,
		Reason:       e.Reason,
		Detail:       detail,
	})
	if err != nil {
		slog.Warn("record queue event failed", "kind", e.Kind, "pr", e.PRNumber, "error", err)
		e.Time = time.Now()
	} else {
		e.ID, e.Time = row.ID, row.CreatedAt.Time
	}

	s.events.mu.RLock()
	repo, observers := s.events.repoNames[e.RepoID], s.events.observers
	s.events.mu.RUnlock()
	for _, o := range observers {
		o(repo, e)
	}
}

// EventFilter selects events for ListEvents. Zero values do not filter.
type EventFilter struct {
	AfterID int64
	RepoID  int64
	Kind    EventKind
	Since   time.Time
	Limit   int32
}

// ListEvents returns events in id order, so AfterID works as a cursor.
func (s *Service) ListEvents(ctx context.Context, f EventFilter) ([]pg.ListEventsRow, error) {
	return s.queries().ListEvents(ctx, pg.ListEventsParams{
		AfterID: f.AfterID,
		RepoID:  f.RepoID,
		Kind:    string(f.Kind),
		Since:   pgtype.Timestamptz{Time: f.Since, Valid: true},
		Lim:     f.Limit,
	})
}

// SummarizeEvents counts events per repo, kind and reason since the given
// time. repoID 0 means all repos.
func (s *Service) SummarizeEvents(ctx context.Context, since time.Time, repoID int64) ([]pg.SummarizeEventsRow, error) {
	return s.queries().SummarizeEvents(ctx, pg.SummarizeEventsParams{
		Since:  pgtype.Timestamptz{Time: since, Valid: true},
		RepoID: repoID,
	})
}

// PruneEvents deletes events older than before and returns how many.
func (s *Service) PruneEvents(ctx context.Context, before time.Time) (int64, error) {
	return s.queries().PruneEvents(ctx, pgtype.Timestamptz{Time: before, Valid: true})
}

// HasEvent reports whether an event of kind was recorded for PR number at
// head.
func (s *Service) HasEvent(ctx context.Context, repoID int64, kind EventKind, number int64, head string) (bool, error) {
	return s.queries().HasEvent(ctx, pg.HasEventParams{
		RepoID: repoID, Kind: string(kind), PrNumber: pgtype.Int8{Int64: number, Valid: true}, HeadSha: head,
	})
}
