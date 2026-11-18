-- +goose Up
-- Allow background commands with wake='none' so callers can run commands or wait for them
-- in-turn with background_wait without scheduling post-turn wakes.
CREATE TABLE background_commands_new (
    id TEXT PRIMARY KEY,
    call_key TEXT NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_request_id TEXT NOT NULL REFERENCES submissions (id),
    wake TEXT NOT NULL CHECK (wake IN ('exit', 'lines', 'none')),
    label TEXT NOT NULL,
    command TEXT NOT NULL,
    cwd TEXT NOT NULL,
    output_path TEXT NOT NULL,
    timeout_ms INTEGER,
    pid INTEGER NOT NULL,
    pgid INTEGER NOT NULL,
    process_started_at INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('starting', 'running', 'exited', 'stopped', 'timed_out', 'unknown')),
    exit_status INTEGER,
    reason TEXT NOT NULL DEFAULT '',
    ended_at INTEGER
) STRICT;

INSERT INTO background_commands_new (
    id, call_key, conversation_id, source_request_id, wake, label, command, cwd, output_path,
    timeout_ms, pid, pgid, process_started_at, state, exit_status, reason, ended_at
)
SELECT
    id, call_key, conversation_id, source_request_id, wake, label, command, cwd, output_path,
    timeout_ms, pid, pgid, process_started_at, state, exit_status, reason, ended_at
FROM background_commands;

DROP INDEX background_commands_running;
DROP INDEX background_commands_conversation;
DROP TABLE background_commands;

ALTER TABLE background_commands_new RENAME TO background_commands;
CREATE INDEX background_commands_conversation ON background_commands (conversation_id, process_started_at);
CREATE INDEX background_commands_running ON background_commands (state) WHERE state = 'running';

-- +goose Down
CREATE TABLE background_commands_old (
    id TEXT PRIMARY KEY,
    call_key TEXT NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_request_id TEXT NOT NULL REFERENCES submissions (id),
    wake TEXT NOT NULL CHECK (wake IN ('exit', 'lines')),
    label TEXT NOT NULL,
    command TEXT NOT NULL,
    cwd TEXT NOT NULL,
    output_path TEXT NOT NULL,
    timeout_ms INTEGER,
    pid INTEGER NOT NULL,
    pgid INTEGER NOT NULL,
    process_started_at INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('starting', 'running', 'exited', 'stopped', 'timed_out', 'unknown')),
    exit_status INTEGER,
    reason TEXT NOT NULL DEFAULT '',
    ended_at INTEGER
) STRICT;

INSERT INTO background_commands_old (
    id, call_key, conversation_id, source_request_id, wake, label, command, cwd, output_path,
    timeout_ms, pid, pgid, process_started_at, state, exit_status, reason, ended_at
)
SELECT
    id, call_key, conversation_id, source_request_id,
    CASE WHEN wake = 'none' THEN 'exit' ELSE wake END,
    label, command, cwd, output_path,
    timeout_ms, pid, pgid, process_started_at, state, exit_status, reason, ended_at
FROM background_commands;

DROP INDEX background_commands_running;
DROP INDEX background_commands_conversation;
DROP TABLE background_commands;

ALTER TABLE background_commands_old RENAME TO background_commands;
CREATE INDEX background_commands_conversation ON background_commands (conversation_id, process_started_at);
CREATE INDEX background_commands_running ON background_commands (state) WHERE state = 'running';
