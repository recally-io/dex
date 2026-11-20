-- +goose Up
-- The execution environment of a session. '' follows the environment of the bot. 'local' or an
-- execution_environments ID is an explicit selection: it keeps its binding when the bot's
-- environment changes. It has no foreign key, because 'local' has no row and a deleted
-- environment must leave the session readable.
ALTER TABLE conversations ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';

-- The environments that a bot can select for a session, in addition to its own environment.
-- 'local' is the control host and has no execution_environments row.
CREATE TABLE agent_environments (
    agent_id TEXT NOT NULL REFERENCES agents (id),
    environment_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (agent_id, environment_id)
) STRICT;

-- +goose StatementBegin
CREATE TRIGGER agent_environments_environment_delete AFTER DELETE ON execution_environments
BEGIN
    DELETE FROM agent_environments WHERE environment_id = OLD.id;
END;
-- +goose StatementEnd

-- One E2B sandbox for each (bot, environment). It replaces agents.environment_sandbox_id, which
-- held one sandbox for the environment of the bot. environment_id has no foreign key: the E2B
-- backend reads the rows of a deleted environment to kill their sandboxes, then deletes them.
CREATE TABLE agent_sandboxes (
    agent_id TEXT NOT NULL REFERENCES agents (id),
    environment_id TEXT NOT NULL,
    sandbox_id TEXT NOT NULL,
    PRIMARY KEY (agent_id, environment_id)
) STRICT;

INSERT INTO agent_sandboxes (agent_id, environment_id, sandbox_id)
SELECT a.id, a.environment_id, a.environment_sandbox_id
FROM agents a JOIN execution_environments e ON e.id = a.environment_id
WHERE a.environment_sandbox_id <> '';

ALTER TABLE agents DROP COLUMN environment_sandbox_id;

-- A runner environment serves at most one active bot, as its environment or on its allowlist.
CREATE VIEW active_runner_refs AS
SELECT a.id AS agent_id, a.environment_id
FROM agents a JOIN execution_environments e ON e.id = a.environment_id AND e.kind = 'runner'
WHERE a.archived_at IS NULL
UNION ALL
SELECT ae.agent_id, ae.environment_id
FROM agent_environments ae
JOIN agents a ON a.id = ae.agent_id AND a.archived_at IS NULL
JOIN execution_environments e ON e.id = ae.environment_id AND e.kind = 'runner';

DROP TRIGGER runner_one_agent_insert;
DROP TRIGGER runner_one_agent_update;

-- +goose StatementBegin
CREATE TRIGGER runner_one_agent_insert BEFORE INSERT ON agents
WHEN NEW.archived_at IS NULL
    AND EXISTS (SELECT 1 FROM active_runner_refs WHERE environment_id = NEW.environment_id)
BEGIN
    SELECT RAISE(ABORT, 'runner environment already has an active bot');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER runner_one_agent_update BEFORE UPDATE OF environment_id, archived_at ON agents
WHEN NEW.archived_at IS NULL
    AND EXISTS (
        SELECT 1
        FROM (
            SELECT NEW.environment_id AS environment_id
            UNION
            SELECT environment_id FROM agent_environments WHERE agent_id = NEW.id
        ) mine
        JOIN active_runner_refs r ON r.environment_id = mine.environment_id AND r.agent_id <> NEW.id
    )
BEGIN
    SELECT RAISE(ABORT, 'runner environment already has an active bot');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER runner_one_agent_allow BEFORE INSERT ON agent_environments
WHEN EXISTS (SELECT 1 FROM active_runner_refs WHERE environment_id = NEW.environment_id AND agent_id <> NEW.agent_id)
BEGIN
    SELECT RAISE(ABORT, 'runner environment already has an active bot');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER runner_one_agent_allow;
DROP TRIGGER runner_one_agent_update;
DROP TRIGGER runner_one_agent_insert;
DROP VIEW active_runner_refs;

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

ALTER TABLE agents ADD COLUMN environment_sandbox_id TEXT NOT NULL DEFAULT '';
UPDATE agents SET environment_sandbox_id = coalesce(
    (SELECT s.sandbox_id FROM agent_sandboxes s WHERE s.agent_id = agents.id AND s.environment_id = agents.environment_id),
    ''
);
DROP TABLE agent_sandboxes;
DROP TRIGGER agent_environments_environment_delete;
DROP TABLE agent_environments;
ALTER TABLE conversations DROP COLUMN environment_id;
