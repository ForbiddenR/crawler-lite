-- +goose Up
-- +goose StatementBegin
CREATE TABLE projects (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT,
    created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A default project so spiders can be created without setting one up first.
INSERT INTO projects (name, description) VALUES ('default', 'Default project');

CREATE TYPE spider_status AS ENUM ('active', 'paused', 'archived');

CREATE TABLE spiders (
    id             BIGSERIAL PRIMARY KEY,
    project_id     BIGINT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    name           TEXT NOT NULL,
    description    TEXT,
    status         spider_status NOT NULL DEFAULT 'active',
    entry_module   TEXT NOT NULL,                            -- "spiders.amazon:PriceSpider"
    source_key     TEXT,                                     -- MinIO key for latest source zip
    source_version INTEGER NOT NULL DEFAULT 0,
    config         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,
    UNIQUE (project_id, name)
);

CREATE INDEX idx_spiders_status ON spiders(status) WHERE deleted_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS spiders;
DROP TYPE IF EXISTS spider_status;
DROP TABLE IF EXISTS projects;
-- +goose StatementEnd
