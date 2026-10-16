-- +goose Up
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
    CHECK (kind IN ('message', 'task_result'));

CREATE TABLE tasks (
    id TEXT PRIMARY KEY REFERENCES submissions (id),
    parent_conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    conversation_id INTEGER NOT NULL UNIQUE REFERENCES conversations (id),
    spawn_key TEXT NOT NULL UNIQUE,
    source_request_id TEXT NOT NULL REFERENCES submissions (id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled', 'interrupted')),
    report TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    ended_at INTEGER
) STRICT;
CREATE INDEX tasks_parent ON tasks (parent_conversation_id, created_at);

-- +goose Down
DROP TABLE tasks;
ALTER TABLE submissions DROP COLUMN kind;
