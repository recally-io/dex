-- +goose Up
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
    CHECK (kind IN ('message', 'task_result'));
ALTER TABLE submissions ADD COLUMN delivery_attempt INTEGER NOT NULL DEFAULT 0;

CREATE TABLE user_tasks (
    id TEXT PRIMARY KEY,
    short_id TEXT NOT NULL UNIQUE,
    parent_conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'needs_input', 'done', 'failed', 'cancelling', 'unknown', 'cancelled')),
    report TEXT NOT NULL DEFAULT '',
    stop_requested_at INTEGER,
    created_at INTEGER NOT NULL,
    ended_at INTEGER
) STRICT;

CREATE TABLE task_stages (
    id TEXT PRIMARY KEY REFERENCES submissions (id),
    user_task_id TEXT NOT NULL REFERENCES user_tasks (id),
    conversation_id INTEGER NOT NULL UNIQUE REFERENCES conversations (id),
    spawn_key TEXT NOT NULL UNIQUE,
    source_request_id TEXT NOT NULL REFERENCES submissions (id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'done', 'needs_input', 'failed', 'cancelling', 'unknown', 'cancelled')),
    report TEXT NOT NULL DEFAULT '',
    current_tool TEXT NOT NULL DEFAULT '',
    last_event_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    ended_at INTEGER
) STRICT;
CREATE INDEX task_stages_goal ON task_stages (user_task_id, created_at);

-- A replayed finish must not replace a later stage or question.
CREATE TABLE task_finishes (
    call_key TEXT PRIMARY KEY,
    user_task_id TEXT NOT NULL REFERENCES user_tasks (id)
) STRICT;

-- The destination is the source conversation, not the hidden execution conversation.
CREATE TABLE task_notices (
    id TEXT PRIMARY KEY,
    user_task_id TEXT NOT NULL REFERENCES user_tasks (id),
    channel_key TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    delivered_parts INTEGER NOT NULL DEFAULT 0,
    delivered_at INTEGER
) STRICT;

ALTER TABLE channel_commands ADD COLUMN delivered_parts INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE channel_commands DROP COLUMN delivered_parts;
DROP TABLE task_notices;
DROP TABLE task_finishes;
DROP TABLE task_stages;
DROP TABLE user_tasks;
ALTER TABLE submissions DROP COLUMN delivery_attempt;
ALTER TABLE submissions DROP COLUMN kind;
