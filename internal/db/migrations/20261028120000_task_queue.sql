-- +goose Up
ALTER TABLE tasks ADD COLUMN modifies_files INTEGER NOT NULL DEFAULT 1;

ALTER TABLE tasks RENAME TO tasks_old;
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
    modifies_files INTEGER NOT NULL DEFAULT 1
) STRICT;
INSERT INTO tasks (id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, status, report, created_at, started_at, ended_at, constraints_json, done_criteria_json, structured_report, modifies_files)
SELECT id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, status, report, created_at, started_at, ended_at, constraints_json, done_criteria_json, structured_report, modifies_files FROM tasks_old;
DROP TABLE tasks_old;
CREATE INDEX tasks_parent ON tasks (parent_conversation_id, created_at);

-- +goose Down
ALTER TABLE tasks RENAME TO tasks_new;
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
    ended_at INTEGER,
    constraints_json TEXT NOT NULL DEFAULT '[]',
    done_criteria_json TEXT NOT NULL DEFAULT '[]',
    structured_report TEXT NOT NULL DEFAULT ''
) STRICT;
-- The old schema has no admitting state; such tasks have not started native work yet.
INSERT INTO tasks SELECT id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, CASE status WHEN 'admitting' THEN 'queued' ELSE status END, report, created_at, started_at, ended_at, constraints_json, done_criteria_json, structured_report FROM tasks_new;
DROP TABLE tasks_new;
CREATE INDEX tasks_parent ON tasks (parent_conversation_id, created_at);
