package batch

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/store/pg"
)

// SelectPrefix preserves FIFO: incompatible work is next, never skipped in
// favour of cheaper arrivals. A lone ready PR is built immediately.
func SelectPrefix(entries []pg.QueueEntry, inputs []forge.BatchInput, now time.Time) (int, string) {
	if len(entries) == 0 {
		return 0, "empty"
	}
	if len(inputs) != len(entries) {
		return 1, "inputs_unavailable"
	}
	files, suites := map[string]bool{}, map[string]bool{}
	seconds := map[string]float64{}
	for i, in := range inputs {
		if i == forge.BatchHardMax {
			return i, "hard_cap"
		}
		if in.Blocked {
			return i, "dependency_or_head_changed"
		}
		if in.HeadSHA != entries[i].PrHeadSha || in.Isolate || len(in.Files) == 0 || len(in.Suites) == 0 {
			return max(1, i), "isolated_or_unknown"
		}
		if i > 0 {
			for _, file := range in.Files {
				if files[file] {
					return i, "overlapping_files"
				}
			}
			// Old work should not incur extra suites while younger work joins it.
			aged := entries[0].EnqueuedAt.Valid && now.Sub(entries[0].EnqueuedAt.Time) >= forge.BatchMaxWait
			shared, added := 0, 0
			sharedSeconds, addedSeconds, measured := 0.0, 0.0, true
			for _, suite := range in.Suites {
				v, ok := in.Seconds[suite]
				if !ok || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
					measured = false
				}
				if suites[suite] {
					shared++
					old, ok := seconds[suite]
					if !ok {
						measured = false
					}
					sharedSeconds += min(old, v)
				} else {
					added++
					addedSeconds += v
				}
			}
			if aged && added > 0 {
				return i, "age_budget"
			}
			if shared == 0 || (measured && addedSeconds > sharedSeconds) || (!measured && added > 0) {
				return i, "marginal_work"
			}
		}
		for _, file := range in.Files {
			files[file] = true
		}
		for _, suite := range in.Suites {
			suites[suite] = true
			if v := in.Seconds[suite]; v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
				seconds[suite] = max(seconds[suite], v)
			}
		}
	}
	return len(entries), "compatible_prefix"
}

func (e *Engine) selectBatch(ctx context.Context, branch string, entries []pg.QueueEntry) (int, map[string]any, error) {
	advisor, ok := e.Forge.(forge.BatchAdvisor)
	if !ok {
		return min(1, len(entries)), map[string]any{"reason": "advisor_unavailable"}, nil
	}
	candidates := make([]forge.BatchCandidate, len(entries))
	for i, ent := range entries {
		candidates[i] = forge.BatchCandidate{Number: ent.PrNumber, HeadSHA: ent.PrHeadSha}
	}
	inputs, err := advisor.BatchInputs(ctx, e.Owner, e.Repo, branch, e.BatchPolicyPath, candidates)
	if err != nil {
		decision := map[string]any{"reason": "inputs_unavailable", "error": err.Error(), "duration_seconds": nil, "failure_rate": nil}
		return min(1, len(entries)), decision, nil
	}
	n, reason := SelectPrefix(entries, inputs, time.Now())
	var required []string
	for _, in := range inputs[:min(n, len(inputs))] {
		required = append(required, in.Suites...)
	}
	slices.Sort(required)
	decision := map[string]any{"reason": reason, "candidate_count": len(entries), "selected_suites": slices.Compact(required), "failure_rate": nil}
	// Durations remain observations per exact PR head, not a throughput claim.
	decision["inputs"] = inputs[:min(n, len(inputs))]
	if n > forge.BatchHardMax {
		return 0, decision, fmt.Errorf("adaptive batch exceeds hard cap")
	}
	return n, decision, nil
}
