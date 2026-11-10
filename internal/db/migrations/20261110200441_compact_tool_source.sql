-- +goose Up
ALTER TABLE compact_operations RENAME TO compact_operations_old;
CREATE TABLE compact_operations (
    operation_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'admitted', 'empty', 'rejected')),
    task_id INTEGER,
    created_at INTEGER NOT NULL,
    tool_source_request_id TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO compact_operations (operation_id, agent_id, conversation_id, status, task_id, created_at)
SELECT operation_id, agent_id, conversation_id, status, task_id, created_at FROM compact_operations_old;
DROP TABLE compact_operations_old;

-- +goose Down
ALTER TABLE compact_operations RENAME TO compact_operations_new;
CREATE TABLE compact_operations (
    operation_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'admitted', 'empty')),
    task_id INTEGER,
    created_at INTEGER NOT NULL
) STRICT;
INSERT INTO compact_operations (operation_id, agent_id, conversation_id, status, task_id, created_at)
SELECT operation_id, agent_id, conversation_id, status, task_id, created_at FROM compact_operations_new;
DROP TABLE compact_operations_new;
