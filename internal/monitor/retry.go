package monitor

import (
	"context"
	"log/slog"

	"github.com/Mic92/gitea-mq/internal/failure"
	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/retry"
)

// defaultRetryLogLines is how much log is matched against retry patterns
// when failure details do not ask for more.
const defaultRetryLogLines = 50

// InfraRetry checks whether the failing checks on sha match an
// infrastructure pattern and, if so, reruns their jobs. It returns the
// matched pattern and the contexts that were rerun; ok is false when nothing
// matched or nothing could be rerun, in which case the caller ejects as
// usual. Retry counting is the caller's job (per entry or per batch build).
func InfraRetry(ctx context.Context, p retry.Policy, fd failure.Options, f forge.Forge, owner, repo, sha string, required []string) (pattern string, rerun []string, checks []failure.Check, ok bool) {
	rr, can := f.(forge.JobRerunner)
	if !p.Enabled() || !can {
		return "", nil, nil, false
	}
	checks = failure.Collect(ctx, failure.Options{
		Enabled:  true,
		LogLines: max(p.LogLines, fd.LogLines, defaultRetryLogLines),
		Secrets:  fd.Secrets,
	}, f, owner, repo, sha, required)
	pattern, ok = p.Match(checks)
	if !ok {
		return "", nil, checks, false
	}
	for _, c := range checks {
		if c.TargetURL == "" {
			continue
		}
		if err := rr.RerunJob(ctx, owner, repo, c.TargetURL); err != nil {
			slog.Warn("infra retry: rerun failed", "check", c.Context, "error", err)
			continue
		}
		p.Tracker.Start(sha, c.Context)
		rerun = append(rerun, c.Context)
	}
	return pattern, rerun, checks, len(rerun) > 0
}
