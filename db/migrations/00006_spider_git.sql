-- +goose Up
-- +goose StatementBegin
-- Spider source is now distributed by master cloning a git URL on every Sync.
-- We store the URL on the spider itself so the API doesn't need to thread it
-- through every request.
ALTER TABLE spiders
    ADD COLUMN git_url    TEXT,
    ADD COLUMN git_branch TEXT NOT NULL DEFAULT 'main',
    ADD COLUMN last_synced_at TIMESTAMPTZ,
    ADD COLUMN last_sync_commit TEXT,
    ADD COLUMN last_sync_error TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE spiders
    DROP COLUMN IF EXISTS last_sync_error,
    DROP COLUMN IF EXISTS last_sync_commit,
    DROP COLUMN IF EXISTS last_synced_at,
    DROP COLUMN IF EXISTS git_branch,
    DROP COLUMN IF EXISTS git_url;
-- +goose StatementEnd
