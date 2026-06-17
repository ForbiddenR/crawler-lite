-- +goose Up
-- +goose StatementBegin
CREATE TYPE task_status AS ENUM (
    'queued',
    'running',
    'succeeded',
    'failed',
    'cancelled',
    'timeout',
    'captcha_blocked'
);

CREATE TYPE task_trigger AS ENUM ('manual', 'schedule', 'retry', 'api');

CREATE TABLE tasks (
    id              BIGSERIAL PRIMARY KEY,
    spider_id       BIGINT NOT NULL REFERENCES spiders(id) ON DELETE RESTRICT,
    parent_task_id  BIGINT REFERENCES tasks(id) ON DELETE SET NULL,
    trigger         task_trigger NOT NULL,
    status          task_status NOT NULL DEFAULT 'queued',
    spider_version  INTEGER NOT NULL,
    worker_id       TEXT,
    queued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    exit_code       INTEGER,
    error           TEXT,
    error_class     TEXT,
    stats           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by      BIGINT REFERENCES users(id) ON DELETE SET NULL,
    triggered_args  JSONB
);

CREATE INDEX idx_tasks_spider_status ON tasks(spider_id, status);
CREATE INDEX idx_tasks_status_queued ON tasks(status, queued_at)
    WHERE status IN ('queued', 'running');
CREATE INDEX idx_tasks_finished_at   ON tasks(finished_at DESC NULLS LAST);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tasks;
DROP TYPE IF EXISTS task_trigger;
DROP TYPE IF EXISTS task_status;
-- +goose StatementEnd
