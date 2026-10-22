package registry_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/gitea"
	"github.com/Mic92/gitea-mq/internal/merge"
	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/registry"
	"github.com/Mic92/gitea-mq/internal/testutil"
)

func newTestRegistry(t *testing.T) (*registry.RepoRegistry, context.Context) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	pool := testutil.TestDB(t)
	queueSvc := queue.NewService(pool)

	forges := forge.NewSet()
	forges.Register(gitea.NewForge(&gitea.MockClient{}, "https://gitea.example.com"))

	deps := &registry.Deps{
		Forges:         forges,
		Queue:          queueSvc,
		PollInterval:   1 * time.Hour, // long interval so pollers don't fire during tests
		CheckTimeout:   1 * time.Hour,
		SuccessTimeout: 5 * time.Minute,
	}

	return registry.New(ctx, deps), ctx
}

func giteaRef(owner, name string) forge.RepoRef {
	return forge.RepoRef{Forge: forge.KindGitea, Owner: owner, Name: name}
}

func TestAddAndLookup(t *testing.T) {
	reg, ctx := newTestRegistry(t)
	ref := giteaRef("org", "app")

	if err := reg.Add(ctx, ref); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m, ok := reg.Lookup("gitea:org/app")
	if !ok {
		t.Fatal("expected repo to be found after Add")
	}
	if m.Ref != ref {
		t.Errorf("expected ref %v, got %v", ref, m.Ref)
	}
	if m.RepoID == 0 {
		t.Error("expected non-zero RepoID")
	}
}

// Registry must key by forge:owner/name and route Add through the correct
// adapter from Deps.Forges; same owner/name on a second forge is distinct.
func TestAdd_RoutesByForgeKind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	pool := testutil.TestDB(t)
	queueSvc := queue.NewService(pool)

	giteaMock := &gitea.MockClient{}
	ghMock := &forge.MockForge{KindVal: forge.KindGithub}

	forges := forge.NewSet()
	forges.Register(gitea.NewForge(giteaMock, "https://gitea.example.com"))
	forges.Register(ghMock)

	reg := registry.New(ctx, &registry.Deps{
		Forges:       forges,
		Queue:        queueSvc,
		PollInterval: 1 * time.Hour,
		CheckTimeout: 1 * time.Hour,
	})

	if err := reg.Add(ctx, forge.RepoRef{Forge: forge.KindGithub, Owner: "org", Name: "app"}); err != nil {
		t.Fatalf("Add github: %v", err)
	}
	if err := reg.Add(ctx, giteaRef("org", "app")); err != nil {
		t.Fatalf("Add gitea: %v", err)
	}

	if !reg.Contains("github:org/app") || !reg.Contains("gitea:org/app") {
		t.Fatal("expected both forge keys present")
	}

	// EnsureRepoSetup must have been routed to the github mock for the github ref.
	if len(ghMock.CallsTo("EnsureRepoSetup")) != 1 {
		t.Errorf("github EnsureRepoSetup calls = %d, want 1", len(ghMock.CallsTo("EnsureRepoSetup")))
	}

	// Unknown forge kind must error, not silently fall back.
	if err := reg.Add(ctx, forge.RepoRef{Forge: "bitbucket", Owner: "o", Name: "n"}); err == nil {
		t.Error("expected error for unregistered forge kind")
	}
}

func TestAddIdempotent(t *testing.T) {
	reg, ctx := newTestRegistry(t)
	ref := giteaRef("org", "app")

	if err := reg.Add(ctx, ref); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := reg.Add(ctx, ref); err != nil {
		t.Fatalf("second Add: %v", err)
	}

	refs := reg.List()
	if len(refs) != 1 {
		t.Errorf("expected 1 repo after double Add, got %d", len(refs))
	}
}

func TestRemove(t *testing.T) {
	reg, ctx := newTestRegistry(t)
	ref := giteaRef("org", "app")

	if err := reg.Add(ctx, ref); err != nil {
		t.Fatalf("Add: %v", err)
	}

	reg.Remove(ref)

	_, ok := reg.Lookup("gitea:org/app")
	if ok {
		t.Error("expected repo to be gone after Remove")
	}
	if reg.Contains("gitea:org/app") {
		t.Error("Contains should return false after Remove")
	}
}

func TestRemoveCleansUpMergeBranchesAndDBEntries(t *testing.T) {
	pool := testutil.TestDB(t)
	queueSvc := queue.NewService(pool)
	mock := &gitea.MockClient{}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	forges := forge.NewSet()
	forges.Register(gitea.NewForge(mock, "https://gitea.example.com"))

	deps := &registry.Deps{
		Forges:         forges,
		Queue:          queueSvc,
		PollInterval:   1 * time.Hour,
		CheckTimeout:   1 * time.Hour,
		SuccessTimeout: 5 * time.Minute,
	}

	reg := registry.New(ctx, deps)
	ref := giteaRef("org", "app")

	if err := reg.Add(ctx, ref); err != nil {
		t.Fatalf("Add: %v", err)
	}

	m, ok := reg.Lookup("gitea:org/app")
	if !ok {
		t.Fatal("repo not found after Add")
	}

	// Simulate a PR in the queue with a merge branch (as if testing is in progress).
	_, err := queueSvc.Enqueue(ctx, m.RepoID, 42, "abc123", "main", false)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	wantBranch := merge.BranchName(42)
	if err := queueSvc.SetMergeBranch(ctx, m.RepoID, 42, wantBranch, "merge-sha"); err != nil {
		t.Fatalf("SetMergeBranch: %v", err)
	}

	// Remove the repo — should clean up merge branches and DB entries.
	reg.Remove(ref)

	// Verify merge branch was deleted via API.
	deleteCalls := mock.CallsTo("DeleteBranch")
	if len(deleteCalls) != 1 {
		t.Fatalf("expected 1 DeleteBranch call, got %d", len(deleteCalls))
	}
	if deleteCalls[0].Args[2] != wantBranch {
		t.Errorf("expected DeleteBranch for %s, got %v", wantBranch, deleteCalls[0].Args[2])
	}

	// Verify DB entries are gone.
	entries, err := queueSvc.ListActiveEntries(ctx, m.RepoID)
	if err != nil {
		t.Fatalf("ListActiveEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 active entries after Remove, got %d", len(entries))
	}
}

func TestRemoveNonExistent(t *testing.T) {
	reg, _ := newTestRegistry(t)
	// Should not panic.
	reg.Remove(giteaRef("org", "nope"))
}

func TestConcurrentAccess(t *testing.T) {
	reg, ctx := newTestRegistry(t)

	var wg sync.WaitGroup

	// Concurrent adds of different repos.
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ref := giteaRef("org", fmt.Sprintf("repo-%d", n))
			_ = reg.Add(ctx, ref)
		}(i)
	}

	// Concurrent reads while adds are happening.
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.List()
			_ = reg.Contains("gitea:org/repo-0")
			_, _ = reg.Lookup("gitea:org/repo-0")
		}()
	}

	wg.Wait()

	if len(reg.List()) != 10 {
		t.Errorf("expected 10 repos, got %d", len(reg.List()))
	}
}

// Non-admin tokens cannot touch branch protection or hooks; with SkipSetup
// the registry must not even try.
func TestAdd_SkipSetup(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip=%v", skip), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			mock := &gitea.MockClient{}
			forges := forge.NewSet()
			forges.Register(gitea.NewForge(mock, "https://gitea.example.com"))
			reg := registry.New(ctx, &registry.Deps{
				Forges:       forges,
				Queue:        queue.NewService(testutil.TestDB(t)),
				ExternalURL:  "https://mq.example.com",
				PollInterval: time.Hour,
				CheckTimeout: time.Hour,
				SkipSetup:    skip,
			})
			if err := reg.Add(ctx, giteaRef("org", "app")); err != nil {
				t.Fatalf("Add: %v", err)
			}
			// Protection may still be read for startup diagnostics; setup
			// itself (hook listing/creation, protection edits) must not run.
			n := len(mock.CallsTo("ListWebhooks")) + len(mock.CallsTo("CreateWebhook")) + len(mock.CallsTo("EditBranchProtection"))
			if skip && n != 0 {
				t.Fatalf("SkipSetup: got %d setup calls, want 0", n)
			}
			if !skip && n == 0 {
				t.Fatal("setup enabled but no setup calls made")
			}
		})
	}
}

// A protection rule requiring a pull_request-only context stalls every
// merge-branch run until timeout; the registry must say so on startup.
func TestAdd_WarnsAboutPullRequestOnlyChecks(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mock := &gitea.MockClient{}
	mock.ListBranchProtectionsFn = func(context.Context, string, string) ([]gitea.BranchProtection, error) {
		return []gitea.BranchProtection{{
			RuleName: "main", EnableStatusCheck: true,
			StatusCheckContexts: []string{"gitea-mq", "ci / build (pull_request)", "ci / lint (push)"},
		}}, nil
	}
	forges := forge.NewSet()
	forges.Register(gitea.NewForge(mock, "https://gitea.example.com"))
	reg := registry.New(ctx, &registry.Deps{
		Forges: forges, Queue: queue.NewService(testutil.TestDB(t)),
		PollInterval: time.Hour, CheckTimeout: time.Hour, SkipSetup: true,
	})
	if err := reg.Add(ctx, giteaRef("org", "app")); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "GITEA_MQ_MERGE_BRANCH_CHECKS") || !strings.Contains(out, "ci / build (pull_request)") {
		t.Fatalf("expected warning naming the check, got:\n%s", out)
	}
	if strings.Contains(out, "ci / lint (push)") {
		t.Fatalf("push context wrongly flagged:\n%s", out)
	}
}
