-- +goose Up
ALTER TABLE conversations RENAME COLUMN worker_conversation_id TO execution_id;
ALTER TABLE submissions ADD COLUMN admitted_at INTEGER;
ALTER TABLE conversations ADD COLUMN abort_pending INTEGER NOT NULL DEFAULT 0 CHECK (abort_pending IN (0, 1));

-- +goose Down
ALTER TABLE conversations DROP COLUMN abort_pending;
ALTER TABLE submissions DROP COLUMN admitted_at;
ALTER TABLE conversations RENAME COLUMN execution_id TO worker_conversation_id;
