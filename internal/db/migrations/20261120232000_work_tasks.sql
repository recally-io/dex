-- +goose Up
CREATE TABLE work_tasks (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  title TEXT NOT NULL,
  lifecycle TEXT NOT NULL DEFAULT 'open' CHECK (lifecycle IN ('open', 'completed', 'cancelled')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX work_tasks_activity ON work_tasks(updated_at DESC, id DESC);

CREATE TABLE work_task_entries (
  id INTEGER PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES work_tasks(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX work_task_entries_task ON work_task_entries(task_id, id);

-- +goose Down
DROP TABLE work_task_entries;
DROP TABLE work_tasks;
