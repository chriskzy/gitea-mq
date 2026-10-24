// Package failure collects what an author needs after a merge-branch build
// failed: every failing required check with its state, description and link
// and, for CI built into the forge (Gitea Actions), the failing job's name and
// log tail. It formats them as one PR comment and as event detail, so nobody
// has to hunt through CI to find out why a PR was ejected.
package failure

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/store/pg"
)

// Options configures collection. The zero value collects nothing.
type Options struct {
	// Enabled turns collection on (GITEA_MQ_FAILURE_DETAILS).
	Enabled bool
	// LogLines is how many trailing log lines to keep per failing job.
	LogLines int
	// Secrets are literal values (tokens, webhook secrets) that must never
	// appear in output, on top of the built-in patterns.
	Secrets []string
}

// maxJobs bounds log fetches per ejection; the first failures are the ones
// worth reading.
const maxJobs = 3

// maxLineLen truncates very long log lines (minified output, base64 blobs).
const maxLineLen = 400

// Check is one failing required check.
type Check struct {
	Context     string   `json:"context"`
	State       string   `json:"state"`
	Description string   `json:"description,omitempty"`
	TargetURL   string   `json:"target_url,omitempty"`
	Job         string   `json:"job,omitempty"`
	LogTail     []string `json:"log_tail,omitempty"`
}

// Collect returns the failing checks on sha among required (all failing
// checks when required is empty). Errors while fetching logs only drop the
// log; a nil result means collection is off or the forge call failed.
func Collect(ctx context.Context, opts Options, f forge.Forge, owner, repo, sha string, required []string) []Check {
	if !opts.Enabled || sha == "" {
		return nil
	}
	states, err := f.GetCheckStates(ctx, owner, repo, sha)
	if err != nil {
		slog.Warn("failure details: get check states failed", "sha", sha, "error", err)
		return nil
	}
	red := newRedactor(opts.Secrets)
	var out []Check
	for name, c := range states {
		if forge.IsOwnContext(name) || (c.State != pg.CheckStateFailure && c.State != pg.CheckStateError) {
			continue
		}
		if len(required) > 0 && !matchesAny(name, required) {
			continue
		}
		out = append(out, Check{
			Context: name, State: string(c.State),
			Description: red.apply(c.Description), TargetURL: c.TargetURL,
		})
	}
	slices.SortFunc(out, func(a, b Check) int { return strings.Compare(a.Context, b.Context) })

	lf, ok := f.(forge.JobLogFetcher)
	if !ok || opts.LogLines <= 0 {
		return out
	}
	for i := range out[:min(len(out), maxJobs)] {
		if out[i].TargetURL == "" {
			continue
		}
		job, log, err := lf.JobLog(ctx, owner, repo, out[i].TargetURL)
		if err != nil {
			if err != forge.ErrNoJob {
				slog.Warn("failure details: fetch job log failed", "check", out[i].Context, "error", err)
			}
			continue
		}
		out[i].Job = job
		out[i].LogTail = red.lines(Tail(log, opts.LogLines))
	}
	return out
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, _ := pathMatch(p, name); ok {
			return true
		}
	}
	return false
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// Tail returns the last n non-empty lines of log, with ANSI escapes removed
// and long lines truncated.
func Tail(log []byte, n int) []string {
	lines := strings.Split(strings.ReplaceAll(string(log), "\r\n", "\n"), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		l := strings.TrimRight(ansi.ReplaceAllString(lines[i], ""), " \t\r")
		if l == "" {
			continue
		}
		if len(l) > maxLineLen {
			l = l[:maxLineLen] + "…"
		}
		out = append(out, l)
	}
	slices.Reverse(out)
	return out
}

// Detail is the event detail for an ejection with failure details.
func Detail(checks []Check) map[string]any {
	if len(checks) == 0 {
		return nil
	}
	return map[string]any{"failures": checks}
}

// maxComment keeps comments well below forge limits (Gitea: 64 KiB).
const maxComment = 32 << 10

// Comment renders headline followed by a table of failing checks and a
// collapsible log tail per job. The output is bounded in size.
func Comment(headline string, checks []Check) string {
	if len(checks) == 0 {
		return headline
	}
	var b strings.Builder
	b.WriteString(headline)
	b.WriteString("\n\n| Check | State | Job | Description |\n|---|---|---|---|\n")
	for _, c := range checks {
		ctxCell := cell(c.Context)
		if c.TargetURL != "" {
			ctxCell = fmt.Sprintf("[%s](%s)", ctxCell, c.TargetURL)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", ctxCell, c.State, cell(c.Job), cell(c.Description))
	}
	for _, c := range checks {
		if len(c.LogTail) == 0 {
			continue
		}
		name := c.Job
		if name == "" {
			name = c.Context
		}
		section := fmt.Sprintf("\n<details><summary>Last %d log lines of <code>%s</code></summary>\n\n~~~text\n%s\n~~~\n\n</details>\n",
			len(c.LogTail), htmlEscape(name), strings.ReplaceAll(strings.Join(c.LogTail, "\n"), "~~~", "~ ~ ~"))
		if b.Len()+len(section) > maxComment {
			b.WriteString("\n_Further log output omitted: comment size limit._\n")
			break
		}
		b.WriteString(section)
	}
	return b.String()
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
