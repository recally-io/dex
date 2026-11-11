-- +goose Up
-- A child session is a conversation with a parent (ADR 0015). Task conversations become children,
-- and the input that a parent sent to a child keeps the request that sent it. The tasks table stays
-- until the web UI reads children from parent_id.
ALTER TABLE conversations ADD COLUMN parent_id INTEGER REFERENCES conversations (id);
CREATE INDEX conversations_parent ON conversations (parent_id) WHERE parent_id IS NOT NULL;
ALTER TABLE submissions ADD COLUMN parent_request_id TEXT REFERENCES submissions (id);

UPDATE conversations SET parent_id = (SELECT t.parent_conversation_id FROM tasks t WHERE t.conversation_id = conversations.id)
WHERE id IN (SELECT conversation_id FROM tasks);
-- A task's own input is a parent turn. The user's later inputs stay user turns.
UPDATE submissions SET parent_request_id = (SELECT t.source_request_id FROM tasks t WHERE t.id = submissions.id)
WHERE id IN (SELECT id FROM tasks);

-- +goose Down
ALTER TABLE submissions DROP COLUMN parent_request_id;
DROP INDEX conversations_parent;
ALTER TABLE conversations DROP COLUMN parent_id;
