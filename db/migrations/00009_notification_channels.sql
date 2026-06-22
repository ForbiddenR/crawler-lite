-- +goose Up
-- +goose StatementBegin
-- Notification channels: outbound fan-out for terminal task events.
--
-- Each row is one (kind, url) destination plus an event filter
-- (e.g. ['failed','captcha_blocked']). The master's task.Service hooks
-- into OnUpdate and asks notify.Service to fan matching events out to
-- every enabled channel.
--
-- `url` is the shoutrrr-format URL (slack://..., telegram://..., etc.)
-- and embeds the secret token — treat the column as sensitive at
-- export time. `kind` is the URL scheme without the colon; we keep it
-- denormalised so the UI can show a chip without re-parsing the URL.
CREATE TABLE notification_channels (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL,
    url           TEXT NOT NULL,
    -- events: subset of {failed, timeout, captcha_blocked, succeeded}.
    -- Stored as JSONB to match the codebase convention (schedules.args,
    -- tasks.stats). The runtime check is "is this kind in the slice".
    events        JSONB NOT NULL DEFAULT '["failed","timeout","captcha_blocked"]'::jsonb,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_by    BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Partial index — the hot path is "load enabled channels" from
-- task.Service.OnUpdate (gRPC read-loop). The service also caches the
-- result for ~5s, so this index sees one query every few seconds at
-- steady state but stays cheap when bursts of terminal events hit.
CREATE INDEX idx_notification_channels_enabled
    ON notification_channels (id) WHERE enabled;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS notification_channels;
-- +goose StatementEnd
