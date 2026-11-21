-- +goose Up
-- A wait with alert = 1 sends Main a Task alert when an event resolves it.
ALTER TABLE work_task_waits ADD COLUMN alert INTEGER NOT NULL DEFAULT 0 CHECK (alert IN (0, 1));

-- +goose Down
ALTER TABLE work_task_waits DROP COLUMN alert;
