package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/gitea"
	"github.com/Mic92/gitea-mq/internal/poller"
	"github.com/Mic92/gitea-mq/internal/queue"
	"github.com/Mic92/gitea-mq/internal/store/pg"
	"github.com/Mic92/gitea-mq/internal/testutil"
)

// A collaborator token with write (not admin) access cannot read branch
// protection on Gitea. With auto-setup off and checks taken from config the
// queue must still enqueue and start testing a PR.
func TestGiteaNonAdminToken(t *testing.T) {
	giteaServer := testutil.GiteaInstance()
	if giteaServer == nil {
		t.Skip("gitea server not available")
	}
	ctx := t.Context()

	admin := testutil.NewGiteaAPI(giteaServer.URL)
	admin.CreateToken(t)

	const repoName = "nonadmin-mq"
	admin.MustDo(t, "POST", "/user/repos", `{"name": "`+repoName+`", "auto_init": false, "default_branch": "main"}`)
	if err := giteaServer.PatchRepoHooks("testuser", repoName); err != nil {
		t.Fatalf("patch hooks: %v", err)
	}
	admin.MustDo(t, "POST", "/repos/testuser/"+repoName+"/contents/README.md", `{"content": "aW5pdA==", "message": "init"}`)
	admin.MustDo(t, "POST", "/repos/testuser/"+repoName+"/contents/f.txt",
		`{"content": "Zg==", "message": "feature", "new_branch": "feature"}`)
	admin.MustDo(t, "POST", "/repos/testuser/"+repoName+"/branch_protections",
		`{"branch_name": "main", "enable_status_check": true, "status_check_contexts": ["ci/build"]}`)

	admin.MustDo(t, "POST", "/admin/users",
		`{"username": "writer", "password": "writerpass123", "email": "writer@test.com", "must_change_password": false}`)
	admin.MustDo(t, "PUT", "/repos/testuser/"+repoName+"/collaborators/writer", `{"permission": "write"}`)
	token := createUserToken(t, giteaServer.URL, "writer", "writerpass123")
	client := gitea.NewHTTPClient(giteaServer.URL, token)

	// Premise: protection is not readable with this token.
	if _, err := client.GetBranchProtection(ctx, "testuser", repoName, "main"); err == nil {
		t.Fatal("expected branch protection read to fail for a non-admin token")
	}

	prBody := admin.MustDo(t, "POST", "/repos/testuser/"+repoName+"/pulls",
		`{"title": "feature", "head": "feature", "base": "main"}`)
	var pr struct {
		Number int64 `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(prBody, &pr); err != nil {
		t.Fatal(err)
	}
	admin.MustDo(t, "POST", "/repos/testuser/"+repoName+"/statuses/"+pr.Head.SHA, `{"context": "ci/build", "state": "success"}`)
	for i := range 10 {
		status, body := admin.Do(t, "POST", "/repos/testuser/"+repoName+"/pulls/"+itoa(pr.Number)+"/merge",
			`{"Do": "merge", "merge_when_checks_succeed": true}`)
		if status >= 200 && status < 300 {
			break
		}
		if i == 9 {
			t.Fatalf("schedule automerge: %d %s", status, body)
		}
		time.Sleep(500 * time.Millisecond)
	}

	svc := queue.NewService(testutil.TestDB(t))
	f := gitea.NewForge(client, giteaServer.URL)
	repo, err := svc.GetOrCreateRepo(ctx, "gitea", "testuser", repoName)
	if err != nil {
		t.Fatal(err)
	}
	deps := &poller.Deps{
		Forge:            f,
		Queue:            svc,
		RepoID:           repo.ID,
		Owner:            "testuser",
		Repo:             repoName,
		FallbackChecks:   []string{"ci/build"},
		ChecksFromConfig: true,
		SuccessTimeout:   5 * time.Minute,
	}
	result, err := poller.PollOnce(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("poll errors with non-admin token: %v", result.Errors)
	}
	head, err := svc.Head(ctx, repo.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.PrNumber != pr.Number || head.State != pg.EntryStateTesting || !head.MergeBranchSha.Valid {
		t.Fatalf("expected PR #%d testing on a merge branch, got %+v", pr.Number, head)
	}
}

func createUserToken(t *testing.T, baseURL, user, pass string) string {
	t.Helper()
	body := []byte(`{"name": "mq-` + user + `", "scopes": ["write:repository", "write:issue", "read:user"]}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/v1/users/"+user+"/tokens", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token for %s: status %d", user, resp.StatusCode)
	}
	var out struct {
		SHA1 string `json:"sha1"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.SHA1
}
