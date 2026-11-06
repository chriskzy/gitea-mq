-- +goose Up
-- Read-only CI observations, independent of queue admission and retries.
CREATE TABLE pr_head_diagnostics (
    repo_id BIGINT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    pr_number BIGINT NOT NULL,
    head_sha TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    signature TEXT NOT NULL,
    failures JSONB NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    observation_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, pr_number)
);

-- +goose Down
DROP TABLE pr_head_diagnostics;
