// Package retry decides when a failed merge-branch check is an
// infrastructure failure (a simulator that did not start, a runner that lost
// its container socket, a flaky download) rather than the PR's fault, and
// reruns it once on the same commit instead of ejecting the PR.
package retry

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Mic92/gitea-mq/internal/failure"
)

// Policy is the configured retry behaviour. The zero value never retries.
type Policy struct {
	// Patterns are matched against failing checks' descriptions and job
	// log tails (GITEA_MQ_INFRA_RETRY_PATTERNS).
	Patterns []*regexp.Regexp
	// Max is how many retries one entry (or batch build) may use.
	Max int
	// LogLines is how much of each failing job's log is matched.
	LogLines int
	// Tracker suppresses the stale failure status between a rerun and the
	// rerun's first status. Shared per repo; must not be nil when enabled.
	Tracker *Tracker
}

// Enabled reports whether any retry can happen.
func (p Policy) Enabled() bool { return len(p.Patterns) > 0 && p.Max > 0 && p.Tracker != nil }

// ParsePatterns compiles one regular expression per non-empty line.
func ParsePatterns(s string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", line, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// Match returns the first pattern found in any failing check's description
// or log tail.
func (p Policy) Match(checks []failure.Check) (string, bool) {
	for _, re := range p.Patterns {
		for _, c := range checks {
			if re.MatchString(c.Description) {
				return re.String(), true
			}
			for _, l := range c.LogTail {
				if re.MatchString(l) {
					return re.String(), true
				}
			}
		}
	}
	return "", false
}

// Grace bounds suppression when the rerun's own pending status is never
// observed (e.g. it failed again between two polls).
const Grace = 2 * time.Minute

// Tracker remembers reruns until the rerun reports. Between the rerun call
// and the rerun's first status, the forge still shows the old failure; a
// poll in that window must not eject the PR.
type Tracker struct {
	mu      sync.Mutex
	pending map[string]time.Time // sha + "\x00" + context -> rerun time
	// Now overrides the clock in tests.
	Now func() time.Time
}

func NewTracker() *Tracker { return &Tracker{pending: map[string]time.Time{}, Now: time.Now} }

func key(sha, checkCtx string) string { return sha + "\x00" + checkCtx }

// Start marks a rerun of checkCtx on sha.
func (t *Tracker) Start(sha, checkCtx string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[key(sha, checkCtx)] = t.Now()
}

// Suppress reports whether a failed status for checkCtx on sha is the stale
// result of a rerun that has not reported yet. Any non-failed status ends
// the suppression, so the rerun's own failure is acted on.
func (t *Tracker) Suppress(sha, checkCtx string, failed bool) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	k := key(sha, checkCtx)
	at, ok := t.pending[k]
	if !ok {
		return false
	}
	if !failed || t.Now().Sub(at) > Grace {
		delete(t.pending, k)
		return false
	}
	return true
}
