-- +goose Up
CREATE TABLE task_steer_receipts (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    parent_conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    task_id TEXT NOT NULL REFERENCES tasks (id),
    request_id TEXT NOT NULL,
    native_request_id TEXT NOT NULL UNIQUE,
    content TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'reconciliation', 'undelivered')),
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE (agent_id, request_id)
) STRICT;
CREATE INDEX task_steer_receipts_task ON task_steer_receipts (task_id, sequence);

-- +goose Down
DROP TABLE task_steer_receipts;
