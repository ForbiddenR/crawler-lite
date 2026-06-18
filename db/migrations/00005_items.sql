-- +goose Up
-- +goose StatementBegin
CREATE TABLE items (
    id           BIGSERIAL PRIMARY KEY,
    task_id      BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    spider_id    BIGINT NOT NULL REFERENCES spiders(id) ON DELETE CASCADE,
    payload      JSONB NOT NULL,
    payload_hash BYTEA,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_items_task            ON items(task_id);
CREATE INDEX idx_items_spider_created  ON items(spider_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS items;
-- +goose StatementEnd
