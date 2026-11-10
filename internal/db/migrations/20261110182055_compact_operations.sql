-- +goose Up
CREATE TABLE compact_operations (
    operation_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'admitted', 'empty')),
    task_id INTEGER,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE compact_operations;
