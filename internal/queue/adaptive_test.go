package queue_test

import (
	"fmt"
	"testing"

	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/testutil"
)

func TestBatchHardCapAtStorageBoundary(t *testing.T) {
	for _, limit := range []int{0, -1, 10, 11, 100} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			svc, ctx, repoID := testutil.TestQueueService(t)
			for n := int64(1); n <= 15; n++ {
				enqueue(t, svc, repoID, false, n)
			}
			b, err := svc.FormBatch(ctx, repoID, "main", limit)
			if err != nil || b == nil || len(b.MemberIds) != 10 {
				t.Fatalf("batch=%+v err=%v", b, err)
			}
		})
	}
}

func TestBatchRejectsChangedCandidateSnapshot(t *testing.T) {
	svc, ctx, repoID := testutil.TestQueueService(t)
	enqueue(t, svc, repoID, false, 1, 2)
	c, err := svc.QueuedCandidates(ctx, repoID, "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Dequeue(ctx, repoID, 1); err != nil {
		t.Fatal(err)
	}
	b, err := svc.FormBatchSelected(ctx, repoID, "main", 1, []int64{c[0].ID})
	if err != nil || b != nil {
		t.Fatalf("stale snapshot admitted: %+v %v", b, err)
	}
	remaining, _ := svc.QueuedCandidates(ctx, repoID, "main", 10)
	if len(remaining) != 1 || remaining[0].PrNumber != 2 {
		t.Fatalf("unrelated entry changed: %+v", remaining)
	}
}

func TestAgedBatchCannotBeStarvedByPriority(t *testing.T) {
	pool := testutil.TestDB(t)
	svc := queue.NewService(pool)
	ctx := t.Context()
	repo, err := svc.GetOrCreateRepo(ctx, "gitea", "org", "app")
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, svc, repo.ID, false, 1)
	if _, err := pool.Exec(ctx, "UPDATE queue_entries SET enqueued_at=NOW()-INTERVAL '91 minutes'"); err != nil {
		t.Fatal(err)
	}
	enqueue(t, svc, repo.ID, true, 2)
	b, err := svc.FormBatch(ctx, repo.ID, "main", 10)
	if err != nil || b == nil {
		t.Fatal(err)
	}
	entries, _ := svc.GetEntriesByIDs(ctx, b.MemberIds)
	if len(entries) != 1 || entries[0].PrNumber != 1 {
		t.Fatalf("aged head starved: %+v", entries)
	}
}
