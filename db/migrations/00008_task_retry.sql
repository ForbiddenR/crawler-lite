-- +goose Up
-- +goose StatementBegin
ALTER TABLE tasks
    ADD COLUMN attempt    INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN not_before TIMESTAMPTZ;

-- Speed up the dispatcher's "queued AND ready" scan. We only ever filter
-- on `not_before` while a task is queued, so a partial index is enough.
CREATE INDEX idx_tasks_queued_not_before
    ON tasks (not_before)
    WHERE status = 'queued';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_tasks_queued_not_before;
ALTER TABLE tasks
    DROP COLUMN IF EXISTS not_before,
    DROP COLUMN IF EXISTS attempt;
-- +goose StatementEnd
