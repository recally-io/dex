-- +goose Up
-- A prompt that the agent runs on a schedule. schedule is the JSON of the API's Schedule, in
-- time_zone. next_due_at is the next scheduled time, NULL when the automation is disabled or has
-- no future time; each check moves it past now, so a time that was missed stays missed.
-- session 'new' starts the chat automation:<id>:<run id> for each run; 'reuse' uses automation:<id>.
CREATE TABLE automations (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    schedule TEXT NOT NULL,
    time_zone TEXT NOT NULL,
    session TEXT NOT NULL CHECK (session IN ('new', 'reuse')),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    next_due_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- One run of an automation. A started run has a message whose ID is the run's ID. due_at is the
-- scheduled time, NULL for a run that the user started; the unique index makes a check that
-- repeats after a crash a no-op.
CREATE TABLE automation_runs (
    id TEXT PRIMARY KEY,
    automation_id TEXT NOT NULL REFERENCES automations (id),
    due_at INTEGER,
    status TEXT NOT NULL CHECK (status IN ('started', 'failed', 'skipped')),
    reason TEXT,
    created_at INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX automation_runs_due ON automation_runs (automation_id, due_at);
CREATE INDEX automation_runs_recent ON automation_runs (automation_id, created_at);

-- +goose Down
DROP TABLE automation_runs;
DROP TABLE automations;
