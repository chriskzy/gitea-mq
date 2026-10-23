-- +goose Up
-- Append-only log of queue transitions. Feeds /api/events and makes queue
-- behaviour (wait time, ejections, bisection cost) measurable after the fact.
CREATE TABLE queue_events (
    id            BIGSERIAL PRIMARY KEY,
    repo_id       BIGINT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL,
    pr_number     BIGINT,
    batch_id      BIGINT,
    target_branch TEXT NOT NULL DEFAULT '',
    head_sha      TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT '',
    detail        JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX queue_events_repo_id_idx ON queue_events (repo_id, id);
CREATE INDEX queue_events_created_at_idx ON queue_events (created_at);

-- +goose Down
DROP TABLE queue_events;
