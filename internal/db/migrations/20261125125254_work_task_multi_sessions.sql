-- +goose Up
ALTER TABLE work_task_sessions RENAME TO work_task_sessions_old;

CREATE TABLE work_task_sessions (
  task_id TEXT NOT NULL REFERENCES work_tasks(id) ON DELETE CASCADE,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
  linked_at INTEGER NOT NULL,
  PRIMARY KEY (task_id, conversation_id)
) STRICT;
CREATE UNIQUE INDEX work_task_sessions_primary ON work_task_sessions(task_id) WHERE is_primary = 1;
CREATE INDEX work_task_sessions_conversation ON work_task_sessions(conversation_id);

-- Old links had no timestamp. Use the Session creation time.
INSERT INTO work_task_sessions (task_id, conversation_id, agent_id, is_primary, linked_at)
SELECT s.task_id, s.conversation_id, c.agent_id, 1, c.created_at
FROM work_task_sessions_old s JOIN conversations c ON c.id = s.conversation_id;
DROP TABLE work_task_sessions_old;

-- +goose Down
ALTER TABLE work_task_sessions RENAME TO work_task_sessions_multi;

CREATE TABLE work_task_sessions (
  task_id TEXT PRIMARY KEY REFERENCES work_tasks(id) ON DELETE CASCADE,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE
) STRICT;

-- The old schema has room for the primary Session only.
INSERT INTO work_task_sessions (task_id, conversation_id)
SELECT task_id, conversation_id FROM work_task_sessions_multi WHERE is_primary = 1;
DROP TABLE work_task_sessions_multi;
