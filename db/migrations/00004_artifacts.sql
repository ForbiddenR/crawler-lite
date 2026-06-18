-- +goose Up
-- +goose StatementBegin
-- Logs are written directly to MinIO as a streaming JSONL file. Postgres only
-- indexes them so we can show line counts and per-level rollups in the UI
-- without scanning the whole object.
CREATE TABLE task_log_index (
    task_id      BIGINT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    log_key      TEXT NOT NULL,
    bytes        BIGINT NOT NULL DEFAULT 0,
    line_count   INTEGER NOT NULL DEFAULT 0,
    level_counts JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE task_screenshots (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    taken_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    name        TEXT NOT NULL,
    url         TEXT,
    storage_key TEXT NOT NULL,
    width       INTEGER,
    height      INTEGER,
    bytes       INTEGER NOT NULL
);
CREATE INDEX idx_shots_task_time ON task_screenshots(task_id, taken_at);

CREATE TABLE task_har (
    task_id       BIGINT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    storage_key   TEXT NOT NULL,
    request_count INTEGER,
    total_bytes   BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS task_har;
DROP TABLE IF EXISTS task_screenshots;
DROP TABLE IF EXISTS task_log_index;
-- +goose StatementEnd
