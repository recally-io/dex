-- name: ListEnvironments :many
SELECT * FROM execution_environments ORDER BY created_at, id;

-- name: GetEnvironment :one
SELECT * FROM execution_environments WHERE id = ?;

-- name: CreateEnvironment :one
INSERT INTO execution_environments (id, name, kind, config, secret, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEnvironment :one
UPDATE execution_environments
SET name = ?, kind = ?, config = ?, secret = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: DeleteEnvironmentIfUnused :execrows
DELETE FROM execution_environments
WHERE execution_environments.id = @id
  AND NOT EXISTS (
      SELECT 1 FROM agents WHERE agents.environment_id = execution_environments.id AND agents.archived_at IS NULL
  );

-- name: ListActiveAgentsByEnvironment :many
SELECT id, name FROM agents
WHERE environment_id = ? AND archived_at IS NULL
ORDER BY created_at, id;

-- name: GetEnvironmentBinding :one
-- The environment that a session of agent_id selects, or the environment of the agent when
-- environment_id is empty. selected_id is 'local' for the control host. allowed is 1 when the
-- selection is the agent's environment or is on its allowlist. found_id is empty when
-- selected_id names a deleted environment.
SELECT
    CAST(selected.id AS TEXT) AS selected_id,
    CAST(
        selected.id = (CASE a.environment_id WHEN '' THEN 'local' ELSE a.environment_id END)
        OR EXISTS (SELECT 1 FROM agent_environments ae WHERE ae.agent_id = a.id AND ae.environment_id = selected.id)
        AS INTEGER
    ) AS allowed,
    CAST(coalesce(e.id, '') AS TEXT) AS found_id,
    CAST(coalesce(e.name, '') AS TEXT) AS name,
    CAST(coalesce(e.kind, '') AS TEXT) AS kind,
    CAST(coalesce(e.config, '') AS TEXT) AS config,
    CAST(coalesce(e.secret, '') AS TEXT) AS secret,
    CAST(coalesce(e.updated_at, 0) AS INTEGER) AS updated_at,
    CAST(coalesce(s.sandbox_id, '') AS TEXT) AS sandbox_id
FROM agents a
JOIN (
    SELECT d.id AS agent_id, CASE WHEN CAST(@environment_id AS TEXT) <> '' THEN CAST(@environment_id AS TEXT) WHEN d.environment_id = '' THEN 'local' ELSE d.environment_id END AS id
    FROM agents d
) selected ON selected.agent_id = a.id
LEFT JOIN execution_environments e ON e.id = selected.id
LEFT JOIN agent_sandboxes s ON s.agent_id = a.id AND s.environment_id = selected.id
WHERE a.id = CAST(@agent_id AS TEXT);

-- name: SetAgentSandbox :exec
INSERT INTO agent_sandboxes (agent_id, environment_id, sandbox_id)
VALUES (@agent_id, @environment_id, @sandbox_id)
ON CONFLICT (agent_id, environment_id) DO UPDATE SET sandbox_id = excluded.sandbox_id;

-- name: ClearAgentSandbox :exec
DELETE FROM agent_sandboxes
WHERE agent_id = @agent_id AND environment_id = @environment_id AND sandbox_id = @sandbox_id;

-- name: GetAgentSandbox :one
SELECT sandbox_id FROM agent_sandboxes WHERE agent_id = ? AND environment_id = ?;

-- name: ListEnvironmentSandboxes :many
SELECT agent_id, sandbox_id FROM agent_sandboxes WHERE environment_id = ?;

-- name: ListAgentEnvironments :many
SELECT environment_id FROM agent_environments WHERE agent_id = ? ORDER BY created_at, environment_id;

-- name: AddAgentEnvironment :exec
INSERT INTO agent_environments (agent_id, environment_id, created_at) VALUES (?, ?, ?)
ON CONFLICT (agent_id, environment_id) DO NOTHING;

-- name: RemoveAgentEnvironment :exec
DELETE FROM agent_environments WHERE agent_id = ? AND environment_id = ?;

-- name: ListAllowedEnvironments :many
-- The environments that sessions of agent_id can select: the environment of the agent first, then
-- its allowlist. 'local' is the control host.
SELECT
    CAST(x.id AS TEXT) AS id,
    CAST(coalesce(e.name, x.id) AS TEXT) AS name,
    CAST(coalesce(e.kind, 'local') AS TEXT) AS kind,
    CAST(x.is_default AS INTEGER) AS is_default
FROM (
    SELECT CASE d.environment_id WHEN '' THEN 'local' ELSE d.environment_id END AS id, 1 AS is_default, 0 AS created_at
    FROM agents d WHERE d.id = @agent_id
    UNION ALL
    SELECT ae.environment_id, 0, ae.created_at
    FROM agent_environments ae JOIN agents d ON d.id = ae.agent_id
    WHERE ae.agent_id = @agent_id
      AND ae.environment_id <> CASE d.environment_id WHEN '' THEN 'local' ELSE d.environment_id END
) x
LEFT JOIN execution_environments e ON e.id = x.id
WHERE x.id = 'local' OR e.id IS NOT NULL
ORDER BY x.is_default DESC, x.created_at, x.id;

-- name: SetChatEnvironment :execrows
-- A chat selects its environment before its first message only.
UPDATE conversations SET environment_id = @environment_id, cwd = ''
WHERE conversations.id = @id AND conversations.agent_id = @agent_id AND conversations.execution_id IS NULL AND conversations.forked_from IS NULL
  AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.conversation_id = conversations.id);

-- name: CountDefaultSessionWork :one
-- Queued input and running background commands in the sessions of agent_id that follow the
-- environment of the agent. A change of that environment waits until they end. Admitted input
-- is not counted: its run started in the earlier environment.
SELECT
    (SELECT count(*) FROM submissions s JOIN conversations c ON c.id = s.conversation_id
     WHERE c.agent_id = @agent_id AND c.environment_id = '' AND s.status = 'pending' AND s.admitted_at IS NULL)
    + (SELECT count(*) FROM background_commands b JOIN conversations c ON c.id = b.conversation_id
       WHERE c.agent_id = @agent_id AND c.environment_id = '' AND b.state IN ('starting', 'running'));

-- name: ClearDefaultSessionCwd :exec
-- The sessions that follow the environment of the agent start in the workspace of the new one.
UPDATE conversations SET cwd = '' WHERE agent_id = ? AND environment_id = '';

-- name: CountPendingAgent :one
SELECT count(*)
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE c.agent_id = ? AND s.status = 'pending';

-- name: CountEnvironmentSessions :one
-- The sessions of agent_id that select environment_id explicitly.
SELECT count(*) FROM conversations WHERE agent_id = ? AND environment_id = ?;
