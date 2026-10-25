package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
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
	beforeRejection, err := svc.AdmissionObservationTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RejectHead(ctx, &first.Entry); err != nil {
		t.Fatal(err)
	}
	if err = svc.Dequeue(ctx, repo.ID, 42); err != nil {
		t.Fatal(err)
	}
	svc = queue.NewService(pool)
	if err = svc.ObserveClearedIntent(ctx, repo.ID, 42, "rejected", beforeRejection); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		got, err := svc.EnqueueAutomatic(ctx, repo.ID, 42, "rejected", "main", false, explicit)
		if err != nil || got != nil {
			t.Fatalf("old intent explicit=%v result=%+v err=%v", explicit, got, err)
		}
	}
	afterRejection, err := svc.AdmissionObservationTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.ObserveClearedIntent(ctx, repo.ID, 42, "rejected", afterRejection); err != nil {
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
	if _, err = svc.RejectHead(ctx, &got.Entry); err != nil {
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

func TestAutomaticAdmissionUsesOneConnectionAndSeesItsPosition(t *testing.T) {
	seed := testutil.TestDB(t)
	config := seed.Config()
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc := queue.NewService(pool)
	repo, err := svc.GetOrCreateRepo(ctx, "gitea", "org", "app")
	if err != nil {
		t.Fatal(err)
	}
	for n := int64(1); n <= 2; n++ {
		got, err := svc.EnqueueAutomatic(ctx, repo.ID, n, "head", "main", false, false)
		if err != nil || got == nil || got.Position != n {
			t.Fatalf("position must come from the admission transaction: result=%+v err=%v", got, err)
		}
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
	if _, err = svc.RejectHead(ctx, &entry.Entry); err == nil {
		t.Fatal("rejection write failure was hidden")
	}
	if existing, err := svc.GetEntry(ctx, repo.ID, 42); err != nil || existing == nil {
		t.Fatalf("entry lost after failed rejection: %+v %v", existing, err)
	}
	if got, err := svc.EnqueueAutomatic(ctx, repo.ID, 43, "other", "main", false, false); err == nil || got != nil {
		t.Fatalf("admission did not fail closed: %+v %v", got, err)
	}
}
