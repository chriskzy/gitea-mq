package failure_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Mic92/gitea-mq/internal/failure"
	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/store/pg"
)

type logForge struct {
	*forge.MockForge
	logs map[string]string // target URL -> log
}

func (f *logForge) JobLog(_ context.Context, _, _, url string) (string, []byte, error) {
	l, ok := f.logs[url]
	if !ok {
		return "", nil, forge.ErrNoJob
	}
	return "build (push)", []byte(l), nil
}

const token = "s3cr3t-gitea-token-value"

func TestCollectAndComment(t *testing.T) {
	mf := &forge.MockForge{}
	mf.GetCheckStatesFn = func(context.Context, string, string, string) (map[string]forge.Check, error) {
		return map[string]forge.Check{
			"ci / build (push)": {State: pg.CheckStateFailure, Description: "Failing after 3m", TargetURL: "https://ci/run/1/jobs/0"},
			"ci / lint (push)":  {State: pg.CheckStateSuccess},
			"external/ci":       {State: pg.CheckStateError, Description: "boom"},
			"gitea-mq":          {State: pg.CheckStateFailure},
		}, nil
	}
	log := strings.Join([]string{
		"\x1b[32mstep 1\x1b[0m", "", "Authorization: token " + token,
		"cloning https://bot:" + token + "@git.example/repo",
		"export API_KEY=abcdef123456", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		"--- FAIL: TestThing (0.01s)", "FAIL",
	}, "\n")
	f := &logForge{MockForge: mf, logs: map[string]string{"https://ci/run/1/jobs/0": log}}

	checks := failure.Collect(context.Background(),
		failure.Options{Enabled: true, LogLines: 6, Secrets: []string{token}},
		f, "o", "r", "sha", []string{"ci / * (push)", "external/ci"})

	if len(checks) != 2 || checks[0].Context != "ci / build (push)" || checks[1].Context != "external/ci" {
		t.Fatalf("checks = %+v", checks)
	}
	c := checks[0]
	if c.Job != "build (push)" || len(c.LogTail) != 6 || c.LogTail[len(c.LogTail)-1] != "FAIL" {
		t.Fatalf("job/log = %q %q", c.Job, c.LogTail)
	}

	comment := failure.Comment("❌ Removed", checks)
	for _, want := range []string{"❌ Removed", "ci / build (push)", "build (push)", "--- FAIL: TestThing", "external/ci", "boom"} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment missing %q:\n%s", want, comment)
		}
	}
	for _, leak := range []string{token, "abcdef123456", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123", "\x1b["} {
		if strings.Contains(comment, leak) {
			t.Errorf("comment leaks %q:\n%s", leak, comment)
		}
	}
	if d := failure.Detail(checks); d["failures"] == nil {
		t.Fatalf("detail = %v", d)
	}
}

func TestCollectDisabled(t *testing.T) {
	if got := failure.Collect(context.Background(), failure.Options{}, &forge.MockForge{}, "o", "r", "sha", nil); got != nil {
		t.Fatalf("disabled collection returned %v", got)
	}
}

func TestCommentBounded(t *testing.T) {
	line := strings.Repeat("x", 390)
	var checks []failure.Check
	for range 10 {
		tail := make([]string, 200)
		for i := range tail {
			tail[i] = line
		}
		checks = append(checks, failure.Check{Context: "c", Job: "j", LogTail: tail})
	}
	if c := failure.Comment("h", checks); len(c) > 40<<10 || !strings.Contains(c, "omitted") {
		t.Fatalf("comment not bounded: %d bytes", len(c))
	}
}

func TestTail(t *testing.T) {
	got := failure.Tail([]byte("a\r\nb\n\n\nc\n"), 2)
	if strings.Join(got, ",") != "b,c" {
		t.Fatalf("Tail = %q", got)
	}
}
