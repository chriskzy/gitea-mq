package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/gitea"
	"github.com/Mic92/gitea-mq/internal/testutil"
)

// No Actions runner is available in tests, so this checks the API surface
// failure details rely on: the endpoints exist, answer in the expected shape,
// and an unknown job degrades to ErrNoJob instead of an error.
func TestGiteaJobLogEndpoints(t *testing.T) {
	gs := testutil.GiteaInstance()
	if gs == nil {
		t.Skip("gitea server not available")
	}
	ctx := t.Context()
	api := testutil.NewGiteaAPI(gs.URL)
	api.CreateToken(t)
	const repoName = "joblog"
	api.MustDo(t, "POST", "/user/repos", `{"name": "`+repoName+`", "auto_init": true, "default_branch": "main"}`)

	if body := string(api.MustDo(t, "GET", "/repos/testuser/"+repoName+"/actions/runs", "")); !strings.Contains(body, `"workflow_runs"`) {
		t.Fatalf("runs response lacks workflow_runs: %s", body)
	}

	c := gitea.NewHTTPClient(gs.URL, api.Token)
	if runs, err := c.ListRuns(ctx, "testuser", repoName); err != nil || len(runs) != 0 {
		t.Fatalf("ListRuns = %v, %v", runs, err)
	}
	if _, err := c.ListRunJobs(ctx, "testuser", repoName, 999); err == nil || !gitea.IsNotFound(err) {
		t.Fatalf("ListRunJobs(unknown) err = %v, want 404", err)
	}
	if _, err := c.GetJobLogs(ctx, "testuser", repoName, 999); err == nil || !gitea.IsNotFound(err) {
		t.Fatalf("GetJobLogs(unknown) err = %v, want 404", err)
	}

	f := gitea.NewForge(c, gs.URL).(forge.JobLogFetcher)
	_, _, err := f.JobLog(ctx, "testuser", repoName, gs.URL+"/testuser/"+repoName+"/actions/runs/999/jobs/0")
	if !errors.Is(err, forge.ErrNoJob) {
		t.Fatalf("JobLog(unknown run) err = %v, want ErrNoJob", err)
	}
}
