-- +goose Up
-- +goose StatementBegin
-- Schedules: cron-driven task creation. Each row is one (spider, cron_expr)
-- pair. The master holds an in-process cron daemon that fires each enabled
-- schedule on cadence and queues a task with `trigger = schedule`.
--
-- last_run_at + last_task_id are populated when the runner fires; next_run_at
-- is updated by the runner whenever the schedule is (re)loaded so the UI can
-- show "next run in 32s" without re-parsing the cron expression client-side.
CREATE TABLE schedules (
    id            BIGSERIAL PRIMARY KEY,
    spider_id     BIGINT NOT NULL REFERENCES spiders(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    cron_expr     TEXT NOT NULL,
    args          JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    last_run_at   TIMESTAMPTZ,
    last_task_id  BIGINT,
    next_run_at   TIMESTAMPTZ,
    created_by    BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_schedules_spider_id ON schedules(spider_id);
-- Partial index — the runner's hot path is "load enabled schedules".
CREATE INDEX idx_schedules_enabled ON schedules(spider_id) WHERE enabled;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS schedules;
-- +goose StatementEnd
