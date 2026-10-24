package retry

import (
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/failure"
)

func TestParsePatterns(t *testing.T) {
	ps, err := ParsePatterns("# comment\n\ndid not return a process handle\n  curl: \\(\\d+\\)  \n")
	if err != nil || len(ps) != 2 {
		t.Fatalf("patterns = %v, %v", ps, err)
	}
	if _, err := ParsePatterns("("); err == nil {
		t.Fatal("invalid regex accepted")
	}
}

func TestMatch(t *testing.T) {
	ps, _ := ParsePatterns("engine.sock: no such file\ncurl: \\(\\d+\\)")
	p := Policy{Patterns: ps, Max: 1, Tracker: NewTracker()}
	if _, ok := p.Match([]failure.Check{{Description: "Failing after 3m", LogTail: []string{"--- FAIL: TestX"}}}); ok {
		t.Fatal("test failure matched")
	}
	if pat, ok := p.Match([]failure.Check{{LogTail: []string{"curl: (56) Recv failure"}}}); !ok || pat != `curl: \(\d+\)` {
		t.Fatalf("log tail not matched: %q", pat)
	}
	if _, ok := p.Match([]failure.Check{{Description: "dial engine.sock: no such file"}}); !ok {
		t.Fatal("description not matched")
	}
}

func TestTracker(t *testing.T) {
	now := time.Unix(0, 0)
	tr := NewTracker()
	tr.Now = func() time.Time { return now }
	tr.Start("sha", "ci")
	if !tr.Suppress("sha", "ci", true) {
		t.Fatal("stale failure not suppressed")
	}
	if tr.Suppress("other", "ci", true) {
		t.Fatal("unrelated sha suppressed")
	}
	if tr.Suppress("sha", "ci", false) || tr.Suppress("sha", "ci", true) {
		t.Fatal("suppression survived the rerun's pending status")
	}
	tr.Start("sha", "ci")
	now = now.Add(Grace + time.Second)
	if tr.Suppress("sha", "ci", true) {
		t.Fatal("suppression outlived grace")
	}
	var nilT *Tracker
	if nilT.Suppress("sha", "ci", true) {
		t.Fatal("nil tracker suppressed")
	}
}
