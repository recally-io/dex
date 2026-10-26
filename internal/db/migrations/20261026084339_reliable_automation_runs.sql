-- +goose Up
ALTER TABLE automations ADD COLUMN cwd TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'changed', 'unchanged', 'failed'));
ALTER TABLE automation_runs ADD COLUMN summary TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN change_key TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN duplicate INTEGER NOT NULL DEFAULT 0 CHECK (duplicate IN (0, 1));

-- Old runs have no structured report, and their outcome stays unknown (''). Only a started run
-- with no message is a known failure: the old code committed the run before its input.
UPDATE automation_runs SET outcome = 'failed', summary = 'the run was stored without its message', status = 'failed',
    reason = 'the run was stored without its message'
WHERE status = 'started' AND NOT EXISTS (SELECT 1 FROM submissions WHERE submissions.id = automation_runs.id);

-- +goose Down
ALTER TABLE automation_runs DROP COLUMN duplicate;
ALTER TABLE automation_runs DROP COLUMN change_key;
ALTER TABLE automation_runs DROP COLUMN summary;
ALTER TABLE automation_runs DROP COLUMN outcome;
ALTER TABLE automations DROP COLUMN cwd;
