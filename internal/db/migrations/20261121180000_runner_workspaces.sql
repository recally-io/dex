-- +goose Up
CREATE TABLE runner_legacy_workspaces (
    environment_id TEXT PRIMARY KEY REFERENCES execution_environments(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE
) STRICT;

-- Only the active bot could use the old workspace when this migration ran.
INSERT INTO runner_legacy_workspaces (environment_id, agent_id)
SELECT e.id, a.id
FROM execution_environments e JOIN agents a ON a.environment_id = e.id
WHERE e.kind = 'runner' AND a.archived_at IS NULL;

DROP TRIGGER runner_one_agent_update;
DROP TRIGGER runner_one_agent_insert;

-- +goose Down
-- Fail rather than restore the old rules over data that violates them.
CREATE TEMP TABLE runner_downgrade_guard (active_count INTEGER CHECK (active_count <= 1));
INSERT INTO runner_downgrade_guard
SELECT count(*) FROM agents a JOIN execution_environments e ON a.environment_id = e.id
WHERE e.kind = 'runner' AND a.archived_at IS NULL GROUP BY e.id;
DROP TABLE runner_downgrade_guard;

-- +goose StatementBegin
CREATE TRIGGER runner_one_agent_insert BEFORE INSERT ON agents
WHEN NEW.archived_at IS NULL
    AND EXISTS (SELECT 1 FROM execution_environments WHERE id = NEW.environment_id AND kind = 'runner')
    AND EXISTS (SELECT 1 FROM agents WHERE environment_id = NEW.environment_id AND archived_at IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'runner environment already has an active bot');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER runner_one_agent_update BEFORE UPDATE OF environment_id, archived_at ON agents
WHEN NEW.archived_at IS NULL
    AND EXISTS (SELECT 1 FROM execution_environments WHERE id = NEW.environment_id AND kind = 'runner')
    AND EXISTS (SELECT 1 FROM agents WHERE environment_id = NEW.environment_id AND archived_at IS NULL AND id <> NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'runner environment already has an active bot');
END;
-- +goose StatementEnd

DROP TABLE runner_legacy_workspaces;
