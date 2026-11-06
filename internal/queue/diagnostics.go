package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Mic92/gitea-mq/internal/failure"
	"github.com/Mic92/gitea-mq/internal/forge"
)

// HeadDiagnosis is the last failed observation of one still-open PR head.
type HeadDiagnosis struct {
	PR               int64               `json:"pr"`
	HeadSHA          string              `json:"head_sha"`
	TargetBranch     string              `json:"target_branch"`
	Signature        string              `json:"-"`
	Failures         []failure.Diagnosis `json:"failures"`
	ObservedAt       time.Time           `json:"observed_at"`
	ObservationError string              `json:"observation_error,omitempty"`
}

// PruneHeadDiagnoses invalidates pushed/closed/retargeted PRs before new log
// collection. The paired arrays prevent a diagnosis matching another PR's SHA.
func (s *Service) PruneHeadDiagnoses(ctx context.Context, repoID int64, prs []forge.PR) error {
	numbers, heads, targets := make([]int64, 0, len(prs)), make([]string, 0, len(prs)), make([]string, 0, len(prs))
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
		heads = append(heads, pr.HeadSHA)
		targets = append(targets, pr.BaseBranch)
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM pr_head_diagnostics d WHERE repo_id=$1 AND NOT EXISTS
		(SELECT 1 FROM unnest($2::bigint[], $3::text[], $4::text[]) AS p(number, head, target) WHERE p.number=d.pr_number AND p.head=d.head_sha AND p.target=d.target_branch)`, repoID, numbers, heads, targets)
	return err
}

// HeadDiagnoses bounds dashboard/API payloads to 1000 PRs. Observations are
// persisted so restarts do not repeat log fetches or failure events.
func (s *Service) HeadDiagnoses(ctx context.Context, repoID int64) ([]HeadDiagnosis, error) {
	rows, err := s.pool.Query(ctx, `SELECT pr_number, head_sha, target_branch, signature, failures, observed_at, observation_error FROM pr_head_diagnostics WHERE repo_id=$1 ORDER BY pr_number LIMIT 1000`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]HeadDiagnosis, 0)
	for rows.Next() {
		var d HeadDiagnosis
		var raw []byte
		if err := rows.Scan(&d.PR, &d.HeadSHA, &d.TargetBranch, &d.Signature, &raw, &d.ObservedAt, &d.ObservationError); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &d.Failures); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SaveHeadDiagnosis returns true only when the persisted failure changed.
func (s *Service) SaveHeadDiagnosis(ctx context.Context, repoID int64, d HeadDiagnosis) (bool, error) {
	data, err := json.Marshal(d.Failures)
	if err != nil {
		return false, err
	}
	var changed bool
	err = s.pool.QueryRow(ctx, `WITH previous AS MATERIALIZED (SELECT head_sha,target_branch,signature,failures FROM pr_head_diagnostics WHERE repo_id=$1 AND pr_number=$2),
		saved AS (INSERT INTO pr_head_diagnostics(repo_id,pr_number,head_sha,target_branch,signature,failures) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(repo_id,pr_number) DO UPDATE SET head_sha=EXCLUDED.head_sha,target_branch=EXCLUDED.target_branch,signature=EXCLUDED.signature,failures=EXCLUDED.failures,observed_at=NOW(),observation_error='')
		SELECT NOT EXISTS(SELECT 1 FROM previous WHERE head_sha=$3 AND target_branch=$4 AND signature=$5 AND failures=$6::jsonb)`, repoID, d.PR, d.HeadSHA, d.TargetBranch, d.Signature, data).Scan(&changed)
	return changed, err
}

// MarkHeadDiagnosisUnavailable retains evidence while clearly withdrawing its
// freshness when the next observation fails. Do not persist raw API errors.
func (s *Service) MarkHeadDiagnosisUnavailable(ctx context.Context, repoID, number int64, head string) error {
	_, err := s.pool.Exec(ctx, `UPDATE pr_head_diagnostics SET observation_error='Head checks unavailable; last observation retained' WHERE repo_id=$1 AND pr_number=$2 AND head_sha=$3`, repoID, number, head)
	return err
}

func (s *Service) ClearHeadDiagnosis(ctx context.Context, repoID, number int64, head string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pr_head_diagnostics WHERE repo_id=$1 AND pr_number=$2 AND head_sha=$3`, repoID, number, head)
	return err
}
