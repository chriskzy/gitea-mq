package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/Mic92/gitea-mq/internal/forge"
)

type batchRouting struct {
	Schema string `json:"schema_version"`
	Checks []struct {
		Producer string   `json:"producer"`
		Paths    []string `json:"paths"`
		Contexts []string `json:"contexts"`
		Required *bool    `json:"required"`
	} `json:"checks"`
	Batch struct {
		Schema         string   `json:"schema_version"`
		SingletonPaths []string `json:"singleton_paths"`
	} `json:"adaptive_batch"`
}

// Only the glob vocabulary currently used by the committed router is accepted.
// Python fnmatch's * includes slashes; Go filepath.Match's * does not.
func batchMatch(pattern, file string) (bool, error) {
	if pattern == "" || strings.ContainsAny(pattern, "?[]\\") {
		return false, fmt.Errorf("unsupported routing pattern")
	}
	r := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	return regexp.MatchString(r, file)
}

func (p batchRouting) input(head string, files []string) (forge.BatchInput, error) {
	in := forge.BatchInput{HeadSHA: head, Files: files, WorkflowPaths: map[string]string{}}
	if p.Schema != "required-check-policy.v1" || p.Batch.Schema != "adaptive-batch.v1" || len(p.Checks) == 0 || len(p.Batch.SingletonPaths) == 0 {
		return in, fmt.Errorf("missing or unsupported batch routing contract")
	}
	for _, file := range files {
		owned := false
		for _, pattern := range p.Batch.SingletonPaths {
			match, err := batchMatch(pattern, file)
			if err != nil {
				return in, err
			}
			in.Isolate = in.Isolate || match
		}
		for _, check := range p.Checks {
			if len(check.Contexts) == 0 || len(check.Paths) == 0 {
				return in, fmt.Errorf("incomplete routing check")
			}
			for _, pattern := range check.Paths {
				match, err := batchMatch(pattern, file)
				if err != nil {
					return in, err
				}
				if match {
					owned = owned || pattern != "**" && pattern != "*"
					if check.Required == nil || *check.Required {
						in.Suites = append(in.Suites, check.Contexts...)
						for _, suite := range check.Contexts {
							workflow, _, ok := strings.Cut(suite, " / ")
							if !ok {
								continue
							}
							producer := check.Producer
							if producer == "" && slices.Contains(check.Paths, ".gitea/workflows/"+workflow+".yml") {
								producer = ".gitea/workflows/" + workflow + ".yml"
							}
							if strings.HasPrefix(producer, ".gitea/workflows/") {
								in.WorkflowPaths[suite] = strings.TrimPrefix(producer, ".gitea/workflows/")
							}
						}
					}
				}
			}
		}
		in.Isolate = in.Isolate || !owned
	}
	slices.Sort(in.Suites)
	in.Suites = slices.Compact(in.Suites)
	return in, nil
}

// readBatchInputs uses the existing bare cache. No PR files are checked out or
// executed; no HTTP pagination can silently truncate the changed-file set.
func (c *HTTPClient) readBatchInputs(ctx context.Context, owner, repo, branch, policyPath string, candidates []forge.BatchCandidate) ([]forge.BatchInput, error) {
	if strings.HasPrefix(policyPath, "/") || strings.Contains(policyPath, "..") || strings.ContainsAny(policyPath, ":\x00\n") {
		return nil, fmt.Errorf("invalid batch policy path")
	}
	refs := []string{"+refs/heads/" + branch + ":refs/remotes/origin/" + branch}
	for _, pr := range candidates {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pr.HeadSHA) {
			return nil, fmt.Errorf("invalid candidate SHA")
		}
		refs = append(refs, pr.HeadSHA)
	}
	var inputs []forge.BatchInput
	err := c.gitCache.withRepo(ctx, c.baseURL+"/"+owner+"/"+repo+".git", owner, repo, refs, func(run func(...string) (string, error)) error {
		base, err := run("rev-parse", "refs/remotes/origin/"+branch)
		if err != nil {
			return err
		}
		base = strings.TrimSpace(base)
		raw, err := run("show", base+":"+policyPath)
		if err != nil {
			return err
		}
		var policy batchRouting
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return err
		}
		for _, pr := range candidates {
			raw, err := run("diff", "--name-only", "--no-renames", "-z", base+"..."+pr.HeadSHA, "--")
			if err != nil {
				return err
			}
			var files []string
			if raw != "" {
				files = strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
			}
			in, err := policy.input(pr.HeadSHA, files)
			if err != nil {
				return err
			}
			in.RoutingBaseSHA = base
			inputs = append(inputs, in)
		}
		return nil
	})
	return inputs, err
}

func (f *giteaForge) BatchInputs(ctx context.Context, owner, repo, branch, policyPath string, candidates []forge.BatchCandidate) ([]forge.BatchInput, error) {
	c, ok := f.client.(*HTTPClient)
	if !ok {
		return nil, fmt.Errorf("batch routing unavailable")
	}
	inputs, err := c.readBatchInputs(ctx, owner, repo, branch, policyPath, candidates)
	if err != nil {
		slog.Warn("batch routing unavailable; isolating candidates", "error", err)
		inputs = make([]forge.BatchInput, len(candidates))
		for i, c := range candidates {
			inputs[i] = forge.BatchInput{HeadSHA: c.HeadSHA, Isolate: true}
		}
	}
	return f.enrichBatchInputs(ctx, owner, repo, branch, candidates, inputs), nil
}

func (f *giteaForge) enrichBatchInputs(ctx context.Context, owner, repo, branch string, candidates []forge.BatchCandidate, inputs []forge.BatchInput) []forge.BatchInput {
	for i, candidate := range candidates {
		pr, err := f.client.GetPR(ctx, owner, repo, candidate.Number)
		if err != nil {
			inputs[i].Blocked = true
			continue
		}
		if pr == nil || pr.Head == nil || pr.Base == nil || pr.Head.Sha != candidate.HeadSHA || pr.Base.Ref != branch || pr.State != "open" || pr.Draft {
			inputs[i].Blocked = true
			continue
		}
		for _, label := range pr.Labels {
			if label != nil && slices.Contains([]string{"batch/singleton", "safety", "authority", "migration", "rollback"}, strings.ToLower(label.Name)) {
				inputs[i].Isolate = true
			}
		}
		deps, err := f.OpenDependencies(ctx, owner, repo, candidate.Number)
		if err != nil {
			inputs[i].Blocked = true
			continue
		}
		inputs[i].Blocked = len(deps) > 0

	}
	// Missing or incomplete native observations remain unavailable. These only
	// advise grouping; the protected gate still validates every selected job.
	runs, err := f.client.ListRuns(ctx, owner, repo)
	if err == nil {
		for i := range inputs {
			inputs[i].Seconds, inputs[i].TimingSources = f.batchDurations(ctx, owner, repo, candidates[i].HeadSHA, inputs[i].Suites, inputs[i].WorkflowPaths, runs)
		}
	}
	return inputs
}

func (f *giteaForge) batchDurations(ctx context.Context, owner, repo, head string, suites []string, workflows map[string]string, runs []ActionRun) (map[string]float64, map[string]string) {
	seconds := map[string]float64{}
	sources := map[string]string{}
	seen := map[string]bool{}
	for _, run := range runs {
		if run.HeadSHA != head || run.Event != "pull_request" {
			continue
		}
		for _, suite := range suites {
			_, job, ok := strings.Cut(suite, " / ")
			if !ok || seen[suite] || workflows[suite] == "" || strings.SplitN(run.Path, "@", 2)[0] != workflows[suite] {
				continue
			}
			seen[suite] = true
			jobs, err := f.client.ListRunJobs(ctx, owner, repo, run.ID)
			if err != nil {
				continue
			}
			for _, j := range jobs {
				if j.RunID != run.ID || j.HeadSHA != head || j.Name != strings.TrimSuffix(job, " (pull_request)") || j.Conclusion != "success" || j.StartedAt.Unix() <= 0 || !j.CompletedAt.After(j.StartedAt) {
					continue
				}
				seconds[suite] = j.CompletedAt.Sub(j.StartedAt).Seconds()
				sources[suite] = j.HTMLURL
			}
		}
	}
	if len(seconds) == 0 {
		return nil, nil
	}
	return seconds, sources
}
