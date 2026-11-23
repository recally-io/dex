-- +goose Up
ALTER TABLE automation_runs ADD COLUMN notification_note TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE automation_runs DROP COLUMN notification_note;
