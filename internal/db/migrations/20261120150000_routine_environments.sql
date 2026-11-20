-- +goose Up
-- The execution environment of a routine's runs: local or a pi-env environment ID. Empty follows
-- the environment of the agent.
ALTER TABLE automations ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';
-- The effective environment and the cwd that a run started with. A reused chat continues only in
-- the environment of its last run, and a result continues where its run ran.
ALTER TABLE automation_runs ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN cwd TEXT NOT NULL DEFAULT '';
-- Before this migration, every run used the environment of its agent and the cwd of its routine.
UPDATE automation_runs SET
    environment_id = coalesce((SELECT CASE WHEN g.environment_id = '' THEN 'local' ELSE g.environment_id END
        FROM automations a JOIN agents g ON g.id = a.agent_id WHERE a.id = automation_runs.automation_id), ''),
    cwd = coalesce((SELECT a.cwd FROM automations a WHERE a.id = automation_runs.automation_id), '');

-- +goose Down
ALTER TABLE automation_runs DROP COLUMN cwd;
ALTER TABLE automation_runs DROP COLUMN environment_id;
ALTER TABLE automations DROP COLUMN environment_id;
