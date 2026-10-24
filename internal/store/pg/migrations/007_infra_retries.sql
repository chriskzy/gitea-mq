-- +goose Up
-- How often an infrastructure failure was retried instead of ejecting: per
-- queue entry (single-PR testing) and per batch build.
ALTER TABLE queue_entries ADD COLUMN infra_retries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE batches ADD COLUMN infra_retries INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE batches DROP COLUMN infra_retries;
ALTER TABLE queue_entries DROP COLUMN infra_retries;
