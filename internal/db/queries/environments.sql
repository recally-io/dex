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

-- name: GetAgentBinding :one
-- found_id is empty when environment_id names a deleted environment.
SELECT
    a.environment_id,
    a.environment_sandbox_id,
    CAST(coalesce(e.id, '') AS TEXT) AS found_id,
    CAST(coalesce(e.kind, '') AS TEXT) AS kind,
    CAST(coalesce(e.config, '') AS TEXT) AS config,
    CAST(coalesce(e.secret, '') AS TEXT) AS secret,
    CAST(coalesce(e.updated_at, 0) AS INTEGER) AS updated_at,
    CAST(coalesce(e.run_as_user, '') AS TEXT) AS run_as_user
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
WHERE id = @agent_id AND environment_sandbox_id = @sandbox_id;

-- name: SetChatEnvironment :execrows
-- A chat selects its environment before its first message only.
UPDATE conversations SET environment_id = @environment_id, cwd = ''
WHERE conversations.id = @id AND conversations.agent_id = @agent_id AND conversations.execution_id IS NULL AND conversations.forked_from IS NULL
  AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.conversation_id = conversations.id);

-- name: CountDefaultSessionWork :one
-- Unsettled input and running background commands in the sessions of agent_id that follow the
-- environment of the agent. A change of that environment waits until they end. Admitted input
-- counts too: PiG can accept input and keep it queued.
SELECT
    (SELECT count(*) FROM submissions s JOIN conversations c ON c.id = s.conversation_id
     WHERE c.agent_id = @agent_id AND c.environment_id = '' AND s.status = 'pending')
    + (SELECT count(*) FROM background_commands b JOIN conversations c ON c.id = b.conversation_id
       WHERE c.agent_id = @agent_id AND c.environment_id = '' AND b.state IN ('starting', 'running'));

-- name: ClearDefaultSessionCwd :exec
-- The sessions that follow the environment of the agent start in the workspace of the new one.
UPDATE conversations SET cwd = '' WHERE agent_id = ? AND environment_id = '';

-- name: ClearDefaultRoutineCwd :exec
-- The routines that follow the environment of the agent run in the workspace of the new one.
UPDATE automations SET cwd = '' WHERE agent_id = ? AND environment_id = '';

-- name: RetireAutomationChat :exec
-- A reused routine chat that ran in another environment moves off the reuse key and stays readable.
UPDATE conversations SET channel_key = @channel_key WHERE id = @id;

-- name: CountPendingAgent :one
SELECT count(*)
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE c.agent_id = ? AND s.status = 'pending';

-- name: LastAutomationRunEnvironment :one
-- The effective environment of the newest run in a routine chat.
SELECT r.environment_id FROM automation_runs r
JOIN submissions s ON s.id = r.id
WHERE s.conversation_id = ?
ORDER BY s.created_at DESC, s.id DESC
LIMIT 1;

-- name: SetEnvironmentRunAsUser :exec
-- Records the account that a machine runs as when it enrolls. Only a runner has one.
UPDATE execution_environments SET run_as_user = @run_as_user WHERE id = @id AND kind = 'runner';

-- name: ClearEnvironmentRunAsUser :exec
-- A revoked machine has no run-as account until the next enrollment reports one.
UPDATE execution_environments SET run_as_user = '' WHERE id = ? AND kind = 'runner';
