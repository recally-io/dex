-- +goose Up
CREATE TABLE runner_pairing (
    environment_id TEXT PRIMARY KEY REFERENCES execution_environments(id) ON DELETE CASCADE,
    code_digest TEXT NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL DEFAULT 0,
    credential_digest TEXT NOT NULL DEFAULT ''
) STRICT;

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

-- +goose Down
DROP TRIGGER runner_one_agent_update;
DROP TRIGGER runner_one_agent_insert;
DROP TABLE runner_pairing;
