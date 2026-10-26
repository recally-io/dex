-- +goose Up
ALTER TABLE automation_runs ADD COLUMN read_at INTEGER;
ALTER TABLE automation_runs ADD COLUMN dismissed_at INTEGER;

-- +goose Down
ALTER TABLE automation_runs DROP COLUMN dismissed_at;
ALTER TABLE automation_runs DROP COLUMN read_at;
