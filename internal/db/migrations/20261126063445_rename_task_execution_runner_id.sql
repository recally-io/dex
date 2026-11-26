-- +goose Up
ALTER TABLE task_executions RENAME COLUMN machine_id TO runner_id;

-- +goose Down
ALTER TABLE task_executions RENAME COLUMN runner_id TO machine_id;
