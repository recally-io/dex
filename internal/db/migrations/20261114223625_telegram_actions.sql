-- +goose Up
ALTER TABLE channel_commands ADD COLUMN rendered_reply TEXT NOT NULL DEFAULT '';
CREATE TABLE telegram_callbacks (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    part INTEGER NOT NULL,
    ordinal INTEGER NOT NULL,
    peer TEXT NOT NULL,
    channel_context TEXT NOT NULL,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    execution_scope TEXT NOT NULL,
    execution_id INTEGER NOT NULL,
    context_revision TEXT NOT NULL,
    label TEXT NOT NULL,
    object_id TEXT NOT NULL,
    action TEXT NOT NULL,
    message_id TEXT NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    UNIQUE(source_id, part, ordinal)
) STRICT;

-- +goose Down
DROP TABLE telegram_callbacks;
ALTER TABLE channel_commands DROP COLUMN rendered_reply;
