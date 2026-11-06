package failure

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Diagnosis is advisory evidence, never an admission or retry decision.
type Diagnosis struct {
	Check
	Classification string `json:"classification"`
	Fingerprint    string `json:"fingerprint"`
	Test           string `json:"test,omitempty"`
	Evidence       string `json:"evidence,omitempty"`
}

var failedTest = regexp.MustCompile(`(?:--- FAIL: ([^\s(]+)|FAILED ([^\s]+)|Test Case '([^']+)' failed)`)
var assertion = regexp.MustCompile(`(?i)(assertion(?:error| failed| failure)|assert(?:equal|true|false|that)\b|expected .+ (?:got|but|actual)|error: .+:[0-9]+: .+ failed)`)
var timestamp = regexp.MustCompile(`\b\d{4}-\d\d-\d\d[T ][0-9:.]+Z?\s*`)
var duration = regexp.MustCompile(`\([0-9.]+s\)`)
var workspace = regexp.MustCompile(`/(?:home/runner/work|workspace|github/workspace|Users/[^/]+/dev)/`)

// Diagnose groups matching failure evidence across PRs. Unknown is intentional:
// a failed check or a generic "error" does not establish a source defect.
func Diagnose(checks []Check, infrastructure []*regexp.Regexp) []Diagnosis {
	out := make([]Diagnosis, 0, len(checks))
	for _, c := range checks {
		d := Diagnosis{Check: c, Classification: "unknown"}
		lines := append([]string{c.Description}, c.LogTail...)
		for _, line := range lines {
			if m := failedTest.FindStringSubmatch(line); m != nil {
				for _, name := range m[1:] {
					if name != "" {
						d.Test = name
						break
					}
				}
				if d.Evidence == "" {
					d.Evidence = line
				}
			}
			if assertion.MatchString(line) {
				d.Classification, d.Evidence = "source", line
			}
		}
		if d.Classification == "unknown" {
			for _, re := range infrastructure {
				for _, line := range lines {
					if re.MatchString(line) {
						d.Classification, d.Evidence = "infrastructure", line
						break
					}
				}
				if d.Classification != "unknown" {
					break
				}
			}
		}
		// Without diagnostic evidence, do not group different unknown log
		// failures merely because their check names happen to match.
		evidence := d.Evidence
		if evidence == "" {
			evidence = strings.Join(lines, "\n")
		}
		normal := timestamp.ReplaceAllString(evidence, "")
		normal = workspace.ReplaceAllString(normal, "<workspace>/")
		normal = duration.ReplaceAllString(normal, "")
		sum := sha256.Sum256([]byte(strings.Join([]string{c.Context, c.Job, d.Classification, d.Test, strings.TrimSpace(normal)}, "\x00")))
		d.Fingerprint = hex.EncodeToString(sum[:16])
		out = append(out, d)
	}
	return out
}
