-- +goose Up
CREATE TABLE work_task_inputs (
  submission_id TEXT PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE,
  task_id TEXT NOT NULL REFERENCES work_tasks(id)
) STRICT;

-- +goose Down
DROP TABLE work_task_inputs;
