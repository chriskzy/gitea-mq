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
			d := failure.Diagnose([]failure.Check{{Context: "ci", LogTail: failure.Tail([]byte(tc.log), 20)}}, infra, 1, "head")[0]
			if d.Classification != tc.want {
				t.Fatalf("%+v", d)
			}
			if tc.name == "gradle summary" && d.Test != "" {
				t.Fatalf("fabricated test: %q", d.Test)
			}
		})
	}
	diagnose := func(line string) failure.Diagnosis {
		return failure.Diagnose([]failure.Check{{Context: "ci", LogTail: []string{"--- FAIL: TestThing (0.01s)", line}}}, nil, 1, "head")[0]
	}
	a := diagnose("2026-11-06T10:10:01Z AssertionError: expected 2 got 1")
	b := diagnose("2026-11-06T11:10:02Z AssertionError: expected 2 got 1")
	c := diagnose("2026-11-06T11:10:02Z AssertionError: expected 3 got 1")
	if a.Test != "TestThing" || a.Fingerprint != b.Fingerprint || a.Fingerprint == c.Fingerprint {
		t.Fatalf("fingerprints: %+v %+v %+v", a, b, c)
	}
	gradle := func(s string) failure.Diagnosis {
		return failure.Diagnose([]failure.Check{{Context: "gradle", LogTail: []string{s}}}, nil, 1, "head")[0]
	}
	if gradle("BUILD FAILED in 12s").Fingerprint != gradle("BUILD FAILED in 23s").Fingerprint {
		t.Fatal("build timing split identical summaries")
	}
}

func TestOnlyPositiveEvidenceCanGroupDifferentHeads(t *testing.T) {
	for _, tc := range []struct {
		description string
		log         []string
		shared      bool
	}{
		{"", nil, false},
		{"Build is failing", nil, false},
		{"", []string{"process exited with code 1"}, false},
		{"AssertionError: expected 2 got 1", nil, true},
		{"", []string{"--- FAIL: TestThing (0.01s)"}, true},
		{"socket unavailable", nil, true},
	} {
		check := []failure.Check{{Context: "ci", Description: tc.description, LogTail: tc.log}}
		patterns := []*regexp.Regexp{regexp.MustCompile("socket unavailable")}
		a := failure.Diagnose(check, patterns, 1, "head")[0]
		b := failure.Diagnose(check, patterns, 2, "head")[0]
		c := failure.Diagnose(check, patterns, 1, "new-head")[0]
		if (a.Fingerprint == b.Fingerprint) != tc.shared || (a.Fingerprint == c.Fingerprint) != tc.shared {
			t.Fatalf("description=%q log=%v shared=%v fingerprints=%s %s %s", tc.description, tc.log, tc.shared, a.Fingerprint, b.Fingerprint, c.Fingerprint)
		}
		if again := failure.Diagnose(check, patterns, 1, "head")[0]; again.Fingerprint != a.Fingerprint {
			t.Fatal("head-scoped fingerprint changed")
		}
	}
}
