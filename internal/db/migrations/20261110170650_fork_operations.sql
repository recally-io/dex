-- +goose Up
CREATE TABLE fork_operations (
    operation_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    source_conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    target_conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    entry_id INTEGER NOT NULL,
    options_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'done', 'rejected')),
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE fork_operations;
