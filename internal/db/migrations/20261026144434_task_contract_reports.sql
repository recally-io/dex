-- +goose Up
ALTER TABLE tasks ADD COLUMN goal TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN constraints_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN done_criteria_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN structured_report TEXT NOT NULL DEFAULT '';
UPDATE tasks SET goal = (SELECT content FROM submissions WHERE submissions.id = tasks.id);

-- +goose Down
ALTER TABLE tasks DROP COLUMN structured_report;
ALTER TABLE tasks DROP COLUMN done_criteria_json;
ALTER TABLE tasks DROP COLUMN constraints_json;
ALTER TABLE tasks DROP COLUMN goal;
