-- +goose Up
-- Settings that the web UI edits. control.sqlite is their only source. The table holds secrets, so
-- the data directory must stay private to the user.

-- Credentials for one pi-ai provider. base_url is used only by "openai-compatible". Migration
-- 00004 replaces this table with providers.
CREATE TABLE provider_settings (
    provider_id TEXT PRIMARY KEY,
    api_key TEXT NOT NULL,
    base_url TEXT NOT NULL DEFAULT ''
) STRICT;

-- The Telegram bot. No row means Telegram is off.
CREATE TABLE telegram_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT NOT NULL,
    -- Telegram user IDs, separated by commas.
    allowed_users TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE telegram_settings;
DROP TABLE provider_settings;
