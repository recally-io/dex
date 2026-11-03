-- +goose Up
CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    display_conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    operation_key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    size INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX artifacts_by_conversation ON artifacts(agent_id, display_conversation_id, created_at);

-- +goose Down
DROP TABLE artifacts;
