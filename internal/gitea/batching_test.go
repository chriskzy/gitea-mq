package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Mic92/gitea-mq/internal/forge"
)

func TestBatchEligibilitySurvivesLaterReadFailure(t *testing.T) {
	f := &giteaForge{client: &MockClient{GetPRFn: func(_ context.Context, _, _ string, n int64) (*PR, error) {
		if n == 2 {
			return nil, errors.New("unavailable")
		}
		return &PR{Index: n, State: "closed"}, nil
	}}}
	candidates := []forge.BatchCandidate{{Number: 1, HeadSHA: "h1"}, {Number: 2, HeadSHA: "h2"}}
	inputs := f.enrichBatchInputs(t.Context(), "o", "r", "main", candidates, make([]forge.BatchInput, 2))
	if !inputs[0].Blocked || !inputs[1].Blocked {
		t.Fatalf("lost eligibility exclusion: %+v", inputs)
	}
}

func TestBatchSemanticBoundaryLabelIsolates(t *testing.T) {
	f := &giteaForge{client: &MockClient{GetPRFn: func(_ context.Context, _, _ string, n int64) (*PR, error) {
		return &PR{Index: n, State: "open", Head: &PRRef{Sha: "h1"}, Base: &PRRef{Ref: "main"}, Labels: []*Label{{Name: "batch/singleton"}}}, nil
	}}}
	inputs := f.enrichBatchInputs(t.Context(), "o", "r", "main", []forge.BatchCandidate{{Number: 1, HeadSHA: "h1"}}, make([]forge.BatchInput, 1))
	if !inputs[0].Isolate || inputs[0].Blocked {
		t.Fatalf("boundary label ignored: %+v", inputs)
	}
}

func TestBatchDurationFromObservedGiteaPayload(t *testing.T) {
	var fixture struct {
		Run  ActionRun   `json:"run"`
		Jobs []ActionJob `json:"jobs"`
	}
	raw, err := os.ReadFile("testdata/batch_native_timing.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	f := &giteaForge{client: &MockClient{ListRunJobsFn: func(_ context.Context, _, _ string, _ int64) ([]ActionJob, error) { return fixture.Jobs, nil }}}
	suite := "ckz-infra-ci / validate (pull_request)"
	workflows := map[string]string{suite: "ckz-infra-ci.yml"}
	seconds, sources := f.batchDurations(t.Context(), "ckz_data_labs", "Terminus", fixture.Run.HeadSHA, []string{suite}, workflows, []ActionRun{fixture.Run})
	if seconds[suite] != 73 || sources[suite] != fixture.Jobs[1].HTMLURL {
		t.Fatalf("unexpected observed timing: %v %v", seconds, sources)
	}
	fixture.Jobs[1].Conclusion = "skipped"
	if seconds, _ := f.batchDurations(t.Context(), "o", "r", fixture.Run.HeadSHA, []string{suite}, workflows, []ActionRun{fixture.Run}); seconds != nil {
		t.Fatal("skipped duration fabricated")
	}
	fixture.Jobs[1].Conclusion = "success"
	if seconds, _ := f.batchDurations(t.Context(), "o", "r", "other-head", []string{suite}, workflows, []ActionRun{fixture.Run}); seconds != nil {
		t.Fatal("different head timing accepted")
	}
}

const batchPolicyFixture = `{"schema_version":"required-check-policy.v1","checks":[{"paths":["**"],"contexts":["contract"]},{"paths":["app/**"],"contexts":["app"]},{"paths":["advisory/**"],"contexts":["advisory"],"required":false}],"adaptive_batch":{"schema_version":"adaptive-batch.v1","singleton_paths":["infra/**","**/migrations/**","**/safety/**","**/rollback/**"]}}`

func TestBatchRoutingConservativeCoverage(t *testing.T) {
	var p batchRouting
	if err := json.Unmarshal([]byte(batchPolicyFixture), &p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file    string
		isolate bool
		suites  []string
	}{
		{"app/deep/code.go", false, []string{"app", "contract"}},
		{"unknown/code.go", true, []string{"contract"}},
		{"advisory/test.go", false, []string{"contract"}},
		{"infra/policy.json", true, []string{"contract"}},
		{"app/migrations/001.sql", true, []string{"app", "contract"}},
		{"app/safety/check.go", true, []string{"app", "contract"}},
		{"app/rollback/revert.sh", true, []string{"app", "contract"}},
	} {
		in, err := p.input("head", []string{tc.file})
		if err != nil || in.Isolate != tc.isolate || !slices.Equal(in.Suites, tc.suites) || in.Seconds != nil {
			t.Fatalf("%s: %+v %v", tc.file, in, err)
		}
	}
	p.Checks[0].Paths = []string{"app/[ab]*"}
	if _, err := p.input("head", []string{"app/a"}); err == nil {
		t.Fatal("unknown glob must fail closed")
	}
}

func TestBatchInputUsesTargetPolicyAndBothRenamePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "org"), 0o755); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(root, "org", "app.git")
	gitIn(t, root, "init", "--bare", "-b", "main", origin)
	work := t.TempDir()
	gitIn(t, work, "init", "-b", "main")
	gitIn(t, work, "remote", "add", "origin", origin)
	commitFile(t, work, "policy.json", batchPolicyFixture, "target policy")
	if err := os.Mkdir(filepath.Join(work, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitFile(t, work, "app/old", "source", "source")
	gitIn(t, work, "push", "origin", "main")
	gitIn(t, work, "checkout", "-b", "feature")
	gitIn(t, work, "mv", "app/old", "app/new")
	// A head cannot replace target routing to exempt itself.
	head := commitFile(t, work, "policy.json", `{}`, "rename and untrusted policy")
	gitIn(t, work, "push", "origin", "feature")
	c := &HTTPClient{baseURL: root, gitCache: newTestCache(t)}
	inputs, err := c.readBatchInputs(t.Context(), "org", "app", "main", "policy.json", []forge.BatchCandidate{{Number: 1, HeadSHA: head}})
	if err != nil || len(inputs) != 1 {
		t.Fatalf("%+v %v", inputs, err)
	}
	if !slices.Equal(inputs[0].Files, []string{"app/new", "app/old", "policy.json"}) || !inputs[0].Isolate {
		t.Fatalf("lost rename or unknown path: %+v", inputs[0])
	}
	if _, err := c.readBatchInputs(t.Context(), "org", "app", "main", "missing.json", []forge.BatchCandidate{{HeadSHA: head}}); err == nil {
		t.Fatal("missing policy accepted")
	}
}
