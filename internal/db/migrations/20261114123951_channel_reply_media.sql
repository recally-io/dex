-- +goose Up
ALTER TABLE telegram_settings ADD COLUMN retry_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE submissions ADD COLUMN channel_context TEXT NOT NULL DEFAULT '{}';
ALTER TABLE submissions ADD COLUMN rendered_reply TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN channel_retry_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE submissions ADD COLUMN channel_delivery_error TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN channel_remote_ids TEXT NOT NULL DEFAULT '[]';
ALTER TABLE submissions ADD COLUMN channel_revoked INTEGER NOT NULL DEFAULT 0;

-- These bytes are temporary outbox payloads, not an artifact catalog.
CREATE TABLE channel_reply_files (
    operation_key TEXT PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    body BLOB NOT NULL CHECK (length(body) <= 20971520)
) STRICT;
CREATE INDEX channel_reply_files_submission ON channel_reply_files(submission_id);

-- +goose StatementBegin
CREATE TRIGGER release_channel_reply_files AFTER UPDATE OF delivered_at, channel_revoked, status ON submissions
WHEN NEW.delivered_at IS NOT NULL OR NEW.channel_revoked = 1 OR NEW.status = 'unanswered'
BEGIN
    DELETE FROM channel_reply_files WHERE submission_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_telegram_replies BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'Telegram authorization revoked'
    WHERE delivered_at IS NULL AND origin LIKE 'telegram:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = OLD.agent_id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_changed_telegram_replies AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'Telegram authorization revoked'
    WHERE delivered_at IS NULL AND origin LIKE 'telegram:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = NEW.agent_id)
      AND (
        coalesce(json_extract(channel_context, '$.accountId'), substr(OLD.token, 1, instr(OLD.token, ':') - 1))
          <> substr(NEW.token, 1, instr(NEW.token, ':') - 1)
        OR instr(',' || replace(NEW.allowed_users, ' ', '') || ',',
          ',' || coalesce(json_extract(channel_context, '$.senderId'), substr(origin, 10)) || ',') = 0
      );
END;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE telegram_settings DROP COLUMN retry_at;
DROP TRIGGER revoke_changed_telegram_replies;
DROP TRIGGER revoke_deleted_telegram_replies;
DROP TRIGGER release_channel_reply_files;
DROP TABLE channel_reply_files;
ALTER TABLE submissions DROP COLUMN channel_revoked;
ALTER TABLE submissions DROP COLUMN channel_remote_ids;
ALTER TABLE submissions DROP COLUMN channel_delivery_error;
ALTER TABLE submissions DROP COLUMN channel_retry_at;
ALTER TABLE submissions DROP COLUMN rendered_reply;
ALTER TABLE submissions DROP COLUMN channel_context;
