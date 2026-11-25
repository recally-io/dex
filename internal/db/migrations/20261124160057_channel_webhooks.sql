-- +goose Up
ALTER TABLE telegram_settings ADD COLUMN webhook_url TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_settings ADD COLUMN webhook_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE feishu_settings ADD COLUMN verification_token TEXT NOT NULL DEFAULT '';
ALTER TABLE feishu_settings ADD COLUMN encrypt_key TEXT NOT NULL DEFAULT '';

CREATE TABLE channel_webhook_inbox (
    id INTEGER PRIMARY KEY,
    platform TEXT NOT NULL,
    account TEXT NOT NULL,
    event_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    received_at INTEGER NOT NULL,
    payload BLOB,
    UNIQUE (platform, account, event_id)
) STRICT;
CREATE INDEX channel_webhook_pending ON channel_webhook_inbox(agent_id, platform, account, id) WHERE payload IS NOT NULL;
CREATE INDEX channel_webhook_handled_received_at ON channel_webhook_inbox(received_at) WHERE payload IS NULL;

-- +goose Down
DROP TABLE channel_webhook_inbox;
ALTER TABLE feishu_settings DROP COLUMN encrypt_key;
ALTER TABLE feishu_settings DROP COLUMN verification_token;
ALTER TABLE telegram_settings DROP COLUMN webhook_secret;
ALTER TABLE telegram_settings DROP COLUMN webhook_url;
