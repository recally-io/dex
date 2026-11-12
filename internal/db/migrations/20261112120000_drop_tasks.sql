-- +goose Up
-- Child sessions replace tasks (ADR 0015). The child_sessions migration copied the parent link of
-- each task conversation and the request of each parent turn, so nothing reads these tables now.
DROP TABLE task_steer_receipts;
DROP TABLE tasks;

-- +goose Down
-- The rows are gone. Down restores the schema only.
CREATE TABLE tasks (
    id TEXT PRIMARY KEY REFERENCES submissions (id),
    parent_conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    conversation_id INTEGER NOT NULL UNIQUE REFERENCES conversations (id),
    spawn_key TEXT NOT NULL UNIQUE,
    source_request_id TEXT NOT NULL REFERENCES submissions (id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'admitting', 'running', 'done', 'failed', 'cancelled', 'interrupted')),
    report TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    ended_at INTEGER,
    constraints_json TEXT NOT NULL DEFAULT '[]',
    done_criteria_json TEXT NOT NULL DEFAULT '[]',
    structured_report TEXT NOT NULL DEFAULT '',
    modifies_files INTEGER NOT NULL DEFAULT 1,
    handled_at INTEGER
) STRICT;
CREATE INDEX tasks_parent ON tasks (parent_conversation_id, created_at);
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
