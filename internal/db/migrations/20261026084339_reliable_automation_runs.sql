-- +goose Up
ALTER TABLE automations ADD COLUMN cwd TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'changed', 'unchanged', 'failed'));
ALTER TABLE automation_runs ADD COLUMN summary TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN change_key TEXT NOT NULL DEFAULT '';
ALTER TABLE automation_runs ADD COLUMN duplicate INTEGER NOT NULL DEFAULT 0 CHECK (duplicate IN (0, 1));

-- Old terminal runs have no structured report. Pending inputs can still report after recovery.
UPDATE automation_runs SET outcome = 'failed', summary = COALESCE(NULLIF(reason, ''), 'run ended before structured reporting was available'), status = 'failed'
WHERE status <> 'skipped' AND NOT EXISTS (SELECT 1 FROM submissions WHERE submissions.id = automation_runs.id AND submissions.status = 'pending');

-- +goose Down
ALTER TABLE automation_runs DROP COLUMN duplicate;
ALTER TABLE automation_runs DROP COLUMN change_key;
ALTER TABLE automation_runs DROP COLUMN summary;
ALTER TABLE automation_runs DROP COLUMN outcome;
ALTER TABLE automations DROP COLUMN cwd;
