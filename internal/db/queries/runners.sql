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
-- found_id is empty when runner_id names a deleted Runner.
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

-- name: SetSessionRunner :execrows
-- Select a Session's Runner before its first message.
UPDATE sessions SET runner_id = @runner_id, cwd = ''
WHERE sessions.id = @id AND sessions.agent_id = @agent_id AND sessions.execution_id IS NULL AND sessions.forked_from IS NULL
  AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.session_id = sessions.id);

-- name: CountDefaultSessionWork :one
-- A change to the Bot's Runner waits for unsettled input and running Processes in
-- Sessions that follow it. This includes input that PiG accepted but has not run.
SELECT
    (SELECT count(*) FROM submissions s JOIN sessions c ON c.id = s.session_id
     WHERE c.agent_id = @agent_id AND c.runner_id = '' AND s.status = 'pending')
    + (SELECT count(*) FROM background_commands b JOIN sessions c ON c.id = b.session_id
       WHERE c.agent_id = @agent_id AND c.runner_id = '' AND b.state IN ('starting', 'running'));

-- name: ClearDefaultSessionCwd :exec
-- Sessions that follow the Bot's Runner start in the new Runner's workspace.
UPDATE sessions SET cwd = '' WHERE agent_id = ? AND runner_id = '';

-- name: ClearDefaultAutomationCwd :exec
-- Automations that follow the Bot's Runner use the new Runner's workspace.
UPDATE automations SET cwd = '' WHERE agent_id = ? AND runner_id = '';

-- name: RetireAutomationSession :exec
-- An Automation Session on another Runner stays readable but can no longer be reused.
UPDATE sessions SET channel_key = @channel_key WHERE id = @id;

-- name: CountPendingAgent :one
SELECT count(*)
FROM submissions s JOIN sessions c ON c.id = s.session_id
WHERE c.agent_id = ? AND s.status = 'pending';

-- name: LastAutomationRunRunner :one
-- The Runner of the newest run in an Automation Session.
SELECT r.runner_id FROM automation_runs r
JOIN submissions s ON s.id = r.id
WHERE s.session_id = ?
ORDER BY s.created_at DESC, s.id DESC
LIMIT 1;
