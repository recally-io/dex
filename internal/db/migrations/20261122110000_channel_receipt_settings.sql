-- +goose Up
ALTER TABLE feishu_settings ADD COLUMN reactions INTEGER CHECK (reactions IN (0, 1));
ALTER TABLE submissions ADD COLUMN required_reply_id TEXT NOT NULL DEFAULT '';
ALTER TABLE outbox_messages ADD COLUMN skipped_part INTEGER NOT NULL DEFAULT 0 CHECK (skipped_part IN (0, 1));

-- +goose Down
ALTER TABLE outbox_messages DROP COLUMN skipped_part;
ALTER TABLE submissions DROP COLUMN required_reply_id;
ALTER TABLE feishu_settings DROP COLUMN reactions;
