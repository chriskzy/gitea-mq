package failure_test

import (
	"regexp"
	"testing"

	"github.com/Mic92/gitea-mq/internal/failure"
)

func TestDiagnosisEvidenceAndGrouping(t *testing.T) {
	infra := []*regexp.Regexp{regexp.MustCompile(`socket unavailable`)}
	for _, tc := range []struct{ name, log, want string }{
		{"test failure alone", "--- FAIL: TestThing (0.01s)", "unknown"},
		{"test infrastructure", "--- FAIL: TestThing (0.01s)\nsocket unavailable", "infrastructure"},
		{"assertion wins", "--- FAIL: TestThing (0.01s)\nAssertionError: expected 2 got 1\nsocket unavailable", "source"},
		{"generic error", "process exited with code 1", "unknown"},
		{"gradle summary", "BUILD FAILED in 12s", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := failure.Diagnose([]failure.Check{{Context: "ci", LogTail: failure.Tail([]byte(tc.log), 20)}}, infra)[0]
			if d.Classification != tc.want {
				t.Fatalf("%+v", d)
			}
			if tc.name == "gradle summary" && d.Test != "" {
				t.Fatalf("fabricated test: %q", d.Test)
			}
		})
	}
	diagnose := func(line string) failure.Diagnosis {
		return failure.Diagnose([]failure.Check{{Context: "ci", LogTail: []string{"--- FAIL: TestThing (0.01s)", line}}}, nil)[0]
	}
	a := diagnose("2026-11-06T10:10:01Z AssertionError: expected 2 got 1")
	b := diagnose("2026-11-06T11:10:02Z AssertionError: expected 2 got 1")
	c := diagnose("2026-11-06T11:10:02Z AssertionError: expected 3 got 1")
	if a.Test != "TestThing" || a.Fingerprint != b.Fingerprint || a.Fingerprint == c.Fingerprint {
		t.Fatalf("fingerprints: %+v %+v %+v", a, b, c)
	}
	gradle := func(s string) failure.Diagnosis {
		return failure.Diagnose([]failure.Check{{Context: "gradle", LogTail: []string{s}}}, nil)[0]
	}
	if gradle("BUILD FAILED in 12s").Fingerprint != gradle("BUILD FAILED in 23s").Fingerprint {
		t.Fatal("build timing split identical summaries")
	}
}
