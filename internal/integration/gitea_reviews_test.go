package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/gitea"
	"github.com/Mic92/gitea-mq/internal/testutil"
)

// Checks the review fields GetReviews relies on (official, stale) against a
// real Gitea: a collaborator's approval counts until new commits are pushed.
func TestGiteaReviews(t *testing.T) {
	giteaServer := testutil.GiteaInstance()
	if giteaServer == nil {
		t.Skip("gitea server not available")
	}
	ctx := t.Context()

	api := testutil.NewGiteaAPI(giteaServer.URL)
	api.CreateToken(t)
	const repoName = "reviews"
	api.MustDo(t, "POST", "/user/repos", `{"name": "`+repoName+`", "auto_init": false, "default_branch": "main"}`)
	if err := giteaServer.PatchRepoHooks("testuser", repoName); err != nil {
		t.Fatal(err)
	}
	api.MustDo(t, "POST", "/repos/testuser/"+repoName+"/contents/README.md", `{"content": "aW5pdA==", "message": "init"}`)
	api.MustDo(t, "POST", "/repos/testuser/"+repoName+"/contents/a.txt", `{"content": "YQ==", "message": "a", "new_branch": "feature"}`)
	api.MustDo(t, "POST", "/repos/testuser/"+repoName+"/branch_protections",
		`{"branch_name": "main", "required_approvals": 1, "dismiss_stale_approvals": true}`)
	api.MustDo(t, "POST", "/admin/users",
		`{"username": "reviewer", "password": "reviewerpass123", "email": "reviewer@test.com", "must_change_password": false}`)
	api.MustDo(t, "PUT", "/repos/testuser/"+repoName+"/collaborators/reviewer", `{"permission": "write"}`)

	var pr struct {
		Number int64 `json:"number"`
	}
	if err := json.Unmarshal(api.MustDo(t, "POST", "/repos/testuser/"+repoName+"/pulls",
		`{"title": "feature", "head": "feature", "base": "main"}`), &pr); err != nil {
		t.Fatal(err)
	}

	f := gitea.NewForge(gitea.NewHTTPClient(giteaServer.URL, api.Token), giteaServer.URL)
	reviews := func() forge.Reviews {
		t.Helper()
		rv, err := forge.GetReviews(ctx, f, "testuser", repoName, pr.Number)
		if err != nil {
			t.Fatal(err)
		}
		return rv
	}
	if rv := reviews(); rv.Approved(1) {
		t.Fatalf("approved before any review: %+v", rv)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		giteaServer.URL+"/api/v1/repos/testuser/"+repoName+"/pulls/"+itoa(pr.Number)+"/reviews",
		strings.NewReader(`{"event": "APPROVED", "body": "lgtm"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("reviewer", "reviewerpass123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d %s", resp.StatusCode, body)
	}
	if rv := reviews(); !rv.Approved(1) {
		t.Fatalf("collaborator approval not counted: %+v", rv)
	}

	// A push makes the approval stale; Gitea marks it asynchronously.
	api.MustDo(t, "POST", "/repos/testuser/"+repoName+"/contents/b.txt", `{"content": "Yg==", "message": "b", "branch": "feature"}`)
	deadline := time.Now().Add(15 * time.Second)
	for reviews().Approved(1) {
		if time.Now().After(deadline) {
			t.Fatal("stale approval still counted after push")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
