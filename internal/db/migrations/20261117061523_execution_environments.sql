-- +goose Up
-- Execution environment definitions shared across bots; local is implicit when environment_id is ''.
CREATE TABLE execution_environments (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    config TEXT NOT NULL DEFAULT '{}',
    secret TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

ALTER TABLE agents ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN environment_sandbox_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agents DROP COLUMN environment_sandbox_id;
ALTER TABLE agents DROP COLUMN environment_id;
DROP TABLE execution_environments;
