-- +goose Up
-- An empty author is an entry from before this column. With event_id, it is external.
ALTER TABLE work_task_entries ADD COLUMN author TEXT NOT NULL DEFAULT '' CHECK (author IN ('', 'owner', 'agent'));

-- The primary work Session of a Task. A Session can be a child of another bot.
CREATE TABLE work_task_sessions (
  task_id TEXT PRIMARY KEY REFERENCES work_tasks(id) ON DELETE CASCADE,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE
) STRICT;

-- A replayed Task tool call returns the stored result and makes no second change.
CREATE TABLE work_task_tool_operations (
  key TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  conversation_id INTEGER NOT NULL,
  source_request_id TEXT NOT NULL,
  action TEXT NOT NULL,
  result TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE work_task_tool_operations;
DROP TABLE work_task_sessions;
ALTER TABLE work_task_entries DROP COLUMN author;
