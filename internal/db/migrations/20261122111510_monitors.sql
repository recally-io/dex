-- +goose Up
DROP TRIGGER cancel_archived_agent_timers;
DROP INDEX followup_timers_due;
DROP INDEX followup_timers_conv;
ALTER TABLE followup_timers RENAME TO monitors;
ALTER TABLE monitors ADD COLUMN check_command TEXT NOT NULL DEFAULT '';
ALTER TABLE monitors ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0 CHECK (interval_seconds = 0 OR interval_seconds BETWEEN 60 AND 604800);
ALTER TABLE monitors ADD COLUMN max_runs INTEGER NOT NULL DEFAULT 1 CHECK (max_runs BETWEEN 1 AND 100);
ALTER TABLE monitors ADD COLUMN runs INTEGER NOT NULL DEFAULT 0 CHECK (runs BETWEEN 0 AND max_runs);
ALTER TABLE monitors ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE monitors ADD COLUMN target_execution_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE monitors ADD COLUMN target_environment TEXT NOT NULL DEFAULT '';
ALTER TABLE monitors ADD COLUMN target_environment_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE monitors ADD COLUMN target_cwd TEXT NOT NULL DEFAULT '';
ALTER TABLE monitors ADD COLUMN output_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE monitors ADD COLUMN outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE monitors ADD COLUMN running INTEGER NOT NULL DEFAULT 0 CHECK (running IN (0, 1));
UPDATE monitors SET runs = 1 WHERE fire_submission_id IS NOT NULL;
CREATE INDEX monitors_due ON monitors (due_at) WHERE state = 'scheduled';
CREATE INDEX monitors_conv ON monitors (conversation_id, created_at DESC);

-- +goose StatementBegin
CREATE TRIGGER cancel_archived_agent_monitors AFTER UPDATE OF archived_at ON agents
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE monitors
    SET state = 'cancelled', outcome = 'agent archived', running = 0, updated_at = NEW.archived_at
    WHERE agent_id = NEW.id AND state = 'scheduled';
END;
-- +goose StatementEnd

-- +goose Down
-- Recurring or script monitors cannot be represented by a timer. Do not arm them on downgrade.
UPDATE monitors SET state = 'cancelled' WHERE state = 'scheduled' AND (max_runs != 1 OR check_command != '' OR running = 1);
DROP TRIGGER cancel_archived_agent_monitors;
DROP INDEX monitors_due;
DROP INDEX monitors_conv;
ALTER TABLE monitors DROP COLUMN running;
ALTER TABLE monitors DROP COLUMN outcome;
ALTER TABLE monitors DROP COLUMN output_hash;
ALTER TABLE monitors DROP COLUMN target_cwd;
ALTER TABLE monitors DROP COLUMN target_environment_revision;
ALTER TABLE monitors DROP COLUMN target_environment;
ALTER TABLE monitors DROP COLUMN target_execution_id;
ALTER TABLE monitors DROP COLUMN expires_at;
ALTER TABLE monitors DROP COLUMN runs;
ALTER TABLE monitors DROP COLUMN max_runs;
ALTER TABLE monitors DROP COLUMN interval_seconds;
ALTER TABLE monitors DROP COLUMN check_command;
ALTER TABLE monitors RENAME TO followup_timers;
CREATE INDEX followup_timers_due ON followup_timers (due_at) WHERE state = 'scheduled';
CREATE INDEX followup_timers_conv ON followup_timers (conversation_id, created_at DESC);

-- +goose StatementBegin
CREATE TRIGGER cancel_archived_agent_timers AFTER UPDATE OF archived_at ON agents
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE followup_timers
    SET state = 'cancelled', updated_at = NEW.archived_at
    WHERE agent_id = NEW.id AND state = 'scheduled';
END;
-- +goose StatementEnd
