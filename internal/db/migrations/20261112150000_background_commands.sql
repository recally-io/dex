-- +goose Up
-- One row per background command of a session. dex stops every running command when it shuts
-- down. A crash leaves rows in starting or running: the next start turns them into unknown,
-- because dex cannot tell whether the process still runs.
CREATE TABLE background_commands (
    id TEXT PRIMARY KEY,
    -- The tool call key. A replayed background_start finds its row and starts no second process.
    call_key TEXT NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    -- The input that started the command. Wake-ups take their origin and parent link from it.
    source_request_id TEXT NOT NULL REFERENCES submissions (id),
    wake TEXT NOT NULL CHECK (wake IN ('exit', 'lines')),
    label TEXT NOT NULL,
    command TEXT NOT NULL,
    cwd TEXT NOT NULL,
    output_path TEXT NOT NULL,
    timeout_ms INTEGER,
    -- The row exists before the process, so a replayed start finds it and never starts a second
    -- process. The PID and the process group ID are zero in state starting.
    pid INTEGER NOT NULL,
    pgid INTEGER NOT NULL,
    -- The time that dex recorded the start, in milliseconds. It is not the start time of the OS
    -- process: it tells a person when the command started, not which process is the command.
    process_started_at INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('starting', 'running', 'exited', 'stopped', 'timed_out', 'unknown')),
    exit_status INTEGER,
    -- Why dex ended the command: stop, timeout, output_limit, rate_limit, or shutdown.
    reason TEXT NOT NULL DEFAULT '',
    ended_at INTEGER
) STRICT;

CREATE INDEX background_commands_conversation ON background_commands (conversation_id, process_started_at);
CREATE INDEX background_commands_running ON background_commands (state) WHERE state = 'running';

-- +goose Down
DROP INDEX background_commands_running;
DROP INDEX background_commands_conversation;
DROP TABLE background_commands;
