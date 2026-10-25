package queue_test

import (
	"testing"

	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/testutil"
)

func TestRejectionPersistsAcrossServicesAndRequiresFreshIntent(t *testing.T) {
	pool := testutil.TestDB(t)
	svc := queue.NewService(pool)
	ctx := t.Context()
	repo, err := svc.GetOrCreateRepo(ctx, "gitea", "org", "app")
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Enqueue(ctx, repo.ID, 42, "rejected", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.RejectHead(ctx, &first.Entry); err != nil {
		t.Fatal(err)
	}
	if err = svc.Dequeue(ctx, repo.ID, 42); err != nil {
		t.Fatal(err)
	}
	svc = queue.NewService(pool)
	for _, explicit := range []bool{false, true} {
		got, err := svc.EnqueueAutomatic(ctx, repo.ID, 42, "rejected", "main", false, explicit)
		if err != nil || got != nil {
			t.Fatalf("old intent explicit=%v result=%+v err=%v", explicit, got, err)
		}
	}
	if err = svc.ObserveClearedIntent(ctx, repo.ID, 42, "rejected"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.EnqueueAutomatic(ctx, repo.ID, 42, "rejected", "main", false, false)
	if err != nil || got != nil {
		t.Fatalf("old approval released rejection: %+v %v", got, err)
	}
	got, err = svc.EnqueueAutomatic(ctx, repo.ID, 42, "rejected", "main", false, true)
	if err != nil || got == nil || !got.IsNew {
		t.Fatalf("fresh explicit reschedule failed: %+v %v", got, err)
	}
	if err = svc.RejectHead(ctx, &got.Entry); err != nil {
		t.Fatal(err)
	}
	if err = svc.Dequeue(ctx, repo.ID, 42); err != nil {
		t.Fatal(err)
	}
	got, err = svc.EnqueueAutomatic(ctx, repo.ID, 42, "new-head", "main", false, false)
	if err != nil || got == nil || !got.IsNew {
		t.Fatalf("changed head blocked: %+v %v", got, err)
	}
}

func TestRejectionStorageFailureFailsClosed(t *testing.T) {
	pool := testutil.TestDB(t)
	svc := queue.NewService(pool)
	ctx := t.Context()
	repo, err := svc.GetOrCreateRepo(ctx, "gitea", "org", "app")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := svc.Enqueue(ctx, repo.ID, 42, "head", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "DROP TABLE rejected_heads"); err != nil {
		t.Fatal(err)
	}
	if err = svc.RejectHead(ctx, &entry.Entry); err == nil {
		t.Fatal("rejection write failure was hidden")
	}
	if existing, err := svc.GetEntry(ctx, repo.ID, 42); err != nil || existing == nil {
		t.Fatalf("entry lost after failed rejection: %+v %v", existing, err)
	}
	if got, err := svc.EnqueueAutomatic(ctx, repo.ID, 43, "other", "main", false, false); err == nil || got != nil {
		t.Fatalf("admission did not fail closed: %+v %v", got, err)
	}
}
