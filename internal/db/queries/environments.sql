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

-- name: GetAgentEnvironmentStamp :one
SELECT
    a.environment_id,
    CAST(coalesce(e.kind, '') AS TEXT) AS kind,
    CAST(coalesce(e.config, '') AS TEXT) AS config,
    CAST(coalesce(e.secret, '') AS TEXT) AS secret,
    CAST(coalesce(e.updated_at, 0) AS INTEGER) AS updated_at
FROM agents a
LEFT JOIN execution_environments e ON e.id = a.environment_id
WHERE a.id = ?;

-- name: SetAgentEnvironmentSandboxID :exec
UPDATE agents
SET environment_sandbox_id = @sandbox_id
WHERE id = @agent_id AND environment_id = @environment_id;

-- name: ClearAgentEnvironmentSandboxID :exec
UPDATE agents
SET environment_sandbox_id = ''
WHERE id = @agent_id;

-- name: CountPendingAgent :one
SELECT count(*)
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE c.agent_id = ? AND s.status = 'pending';
