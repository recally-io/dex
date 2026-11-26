-- name: ListRunners :many
SELECT * FROM runners ORDER BY created_at, id;

-- name: GetRunner :one
SELECT * FROM runners WHERE id = ?;

-- name: CreateRunner :one
INSERT INTO runners (id, name, kind, config, secret, created_at, updated_at, description)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateRunner :one
UPDATE runners
SET name = @name, kind = @kind, config = @config, secret = @secret, updated_at = @updated_at,
    description = coalesce(sqlc.narg(description), description)
WHERE id = @id
RETURNING *;

-- name: DeleteRunnerIfUnused :execrows
DELETE FROM runners
WHERE runners.id = @id
  AND NOT EXISTS (
      SELECT 1 FROM agents WHERE agents.runner_id = runners.id AND agents.archived_at IS NULL
  );

-- name: ListActiveAgentsByRunner :many
SELECT id, name FROM agents
WHERE runner_id = ? AND archived_at IS NULL
ORDER BY created_at, id;

-- name: GetAgentBinding :one
-- found_id is empty when runner_id names a deleted environment.
SELECT
    a.runner_id,
    a.runner_sandbox_id,
    CAST(coalesce(e.id, '') AS TEXT) AS found_id,
    CAST(coalesce(e.kind, '') AS TEXT) AS kind,
    CAST(coalesce(e.config, '') AS TEXT) AS config,
    CAST(coalesce(e.secret, '') AS TEXT) AS secret,
    CAST(coalesce(e.updated_at, 0) AS INTEGER) AS updated_at
FROM agents a
LEFT JOIN runners e ON e.id = a.runner_id
WHERE a.id = ?;

-- name: SetAgentRunnerSandboxID :exec
UPDATE agents
SET runner_sandbox_id = @sandbox_id
WHERE id = @agent_id AND runner_id = @runner_id;

-- name: ClearAgentRunnerSandboxID :exec
UPDATE agents
SET runner_sandbox_id = ''
WHERE id = @agent_id AND runner_sandbox_id = @sandbox_id;

-- name: SetChatRunner :execrows
-- A chat selects its environment before its first message only.
UPDATE conversations SET runner_id = @runner_id, cwd = ''
WHERE conversations.id = @id AND conversations.agent_id = @agent_id AND conversations.execution_id IS NULL AND conversations.forked_from IS NULL
  AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.conversation_id = conversations.id);

-- name: CountDefaultSessionWork :one
-- Unsettled input and running background commands in the sessions of agent_id that follow the
-- environment of the agent. A change of that environment waits until they end. Admitted input
-- counts too: PiG can accept input and keep it queued.
SELECT
    (SELECT count(*) FROM submissions s JOIN conversations c ON c.id = s.conversation_id
     WHERE c.agent_id = @agent_id AND c.runner_id = '' AND s.status = 'pending')
    + (SELECT count(*) FROM background_commands b JOIN conversations c ON c.id = b.conversation_id
       WHERE c.agent_id = @agent_id AND c.runner_id = '' AND b.state IN ('starting', 'running'));

-- name: ClearDefaultSessionCwd :exec
-- The sessions that follow the environment of the agent start in the workspace of the new one.
UPDATE conversations SET cwd = '' WHERE agent_id = ? AND runner_id = '';

-- name: ClearDefaultAutomationCwd :exec
-- The routines that follow the environment of the agent run in the workspace of the new one.
UPDATE automations SET cwd = '' WHERE agent_id = ? AND runner_id = '';

-- name: RetireAutomationChat :exec
-- A reused routine chat that ran in another environment moves off the reuse key and stays readable.
UPDATE conversations SET channel_key = @channel_key WHERE id = @id;

-- name: CountPendingAgent :one
SELECT count(*)
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE c.agent_id = ? AND s.status = 'pending';

-- name: LastAutomationRunRunner :one
-- The effective environment of the newest run in a routine chat.
SELECT r.runner_id FROM automation_runs r
JOIN submissions s ON s.id = r.id
WHERE s.conversation_id = ?
ORDER BY s.created_at DESC, s.id DESC
LIMIT 1;
