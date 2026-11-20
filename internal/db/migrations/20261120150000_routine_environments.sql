-- +goose Up
-- The execution environment of a routine's runs: local or a pi-env environment ID. Empty follows
-- the environment of the agent.
ALTER TABLE automations ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';
-- The effective environment and the cwd that a run started with. A reused chat continues only in
-- the environment of its last run, and a result continues where its run ran.
ALTER TABLE automation_runs ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN cwd TEXT NOT NULL DEFAULT '';
-- Runs before this migration keep an empty binding: their environment and cwd are not known.
-- A reused chat with such a run starts again, and its results cannot be continued from the run.

-- +goose Down
ALTER TABLE automation_runs DROP COLUMN cwd;
ALTER TABLE automation_runs DROP COLUMN environment_id;
ALTER TABLE automations DROP COLUMN environment_id;
