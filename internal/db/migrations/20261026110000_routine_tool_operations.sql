-- +goose Up
CREATE TABLE routine_tool_operations (
    key TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    source_request_id TEXT NOT NULL REFERENCES submissions(id),
    action TEXT NOT NULL,
    result TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE routine_tool_operations;
