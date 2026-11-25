-- +goose Up
CREATE TABLE task_dependencies (
  task_id TEXT NOT NULL REFERENCES work_tasks(id),
  depends_on_task_id TEXT NOT NULL REFERENCES work_tasks(id),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (task_id, depends_on_task_id),
  CHECK (task_id <> depends_on_task_id)
) STRICT;
CREATE INDEX task_dependencies_upstream ON task_dependencies(depends_on_task_id, task_id);
CREATE INDEX work_tasks_parent ON work_tasks(parent_task_id);

-- +goose Down
DROP INDEX work_tasks_parent;
DROP TABLE task_dependencies;
