-- +goose Up
CREATE TABLE rejected_heads (
    repo_id BIGINT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    pr_number BIGINT NOT NULL,
    head_sha TEXT NOT NULL,
    target_branch TEXT NOT NULL,
	entry_id BIGINT NOT NULL,
	rejected_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    intent_cleared BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (repo_id, pr_number, head_sha, target_branch)
);

-- +goose Down
DROP TABLE rejected_heads;
