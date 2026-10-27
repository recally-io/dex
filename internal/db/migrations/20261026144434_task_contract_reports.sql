-- +goose Up
ALTER TABLE tasks ADD COLUMN constraints_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN done_criteria_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN structured_report TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tasks DROP COLUMN structured_report;
ALTER TABLE tasks DROP COLUMN done_criteria_json;
ALTER TABLE tasks DROP COLUMN constraints_json;
