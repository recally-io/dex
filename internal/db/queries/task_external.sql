-- name: CreateExternalExecution :one
INSERT INTO task_external_runs (id, task_id, bot_id, harness, runner_id, host_meta_json,
  workspace_path, git_branch, external_session_id, lease_epoch, lease_expires_at, last_heartbeat_at,
  started_at, dispatch_id, runner_enrollment, lease_ttl_ms)
VALUES (@id, @task_id, @bot_id, @harness, @runner_id, @host_meta_json,
  @workspace_path, @git_branch, @external_session_id, 1, @lease_expires_at, @at, @at, @dispatch_id, @runner_enrollment, @lease_ttl_ms)
RETURNING *;

-- name: GetExternalExecution :one
SELECT * FROM task_external_runs WHERE id = ?;

-- name: GetTaskDispatch :one
SELECT EXISTS (SELECT 1 FROM task_execution_tokens WHERE dispatch_id = ? AND dispatch_id <> '');

-- name: ResumeExternalExecution :one
UPDATE task_external_runs SET state = 'running', bot_id = @bot_id, lease_epoch = lease_epoch + 1,
  lease_expires_at = @lease_expires_at, last_heartbeat_at = @at, ended_at = NULL,
  dispatch_id = @dispatch_id, lease_ttl_ms = @lease_ttl_ms
WHERE id = @id AND state IN ('running', 'stale') RETURNING *;

-- name: InsertTaskToken :exec
INSERT INTO task_execution_tokens (token_hash, task_id, execution_id, lease_epoch, created_at, dispatch_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: RevokeTaskTokens :exec
UPDATE task_execution_tokens SET revoked_at = @at WHERE execution_id = @execution_id AND revoked_at IS NULL;

-- name: GetTaskToken :one
SELECT * FROM task_execution_tokens WHERE token_hash = ?;

-- name: WriteExternalExecution :one
UPDATE task_external_runs SET
  state = coalesce(sqlc.narg(new_state), state),
  ended_at = CASE WHEN sqlc.narg(new_state) IS NOT NULL THEN @at ELSE ended_at END,
  last_heartbeat_at = CASE WHEN @heartbeat THEN @at ELSE last_heartbeat_at END,
  lease_expires_at = CASE WHEN @heartbeat THEN @at + coalesce(CAST(sqlc.narg(ttl_ms) AS INTEGER), lease_ttl_ms) ELSE lease_expires_at END,
  lease_ttl_ms = coalesce(CAST(sqlc.narg(ttl_ms) AS INTEGER), lease_ttl_ms),
  git_branch = coalesce(sqlc.narg(branch), git_branch),
  external_session_id = coalesce(sqlc.narg(session_id), external_session_id)
WHERE id = @id AND task_id = @task_id AND state = 'running'
  AND lease_epoch = @epoch AND lease_expires_at > @at
RETURNING *;

-- name: ListExternalTaskEntries :many
SELECT e.* FROM task_entries e
WHERE e.task_id = @task_id AND e.seq > @after_seq
  AND e.kind NOT IN ('external', 'tombstone') AND e.trust <> 'external'
  AND NOT EXISTS (SELECT 1 FROM task_entries r WHERE r.task_id = e.task_id AND r.kind = 'tombstone'
    AND CAST(json_extract(r.meta_json, '$.targetSeq') AS INTEGER) = e.seq)
ORDER BY e.seq LIMIT @page_size;

-- name: ExtendActiveExternalLeases :exec
UPDATE task_external_runs SET lease_expires_at = max(lease_expires_at, @expires_at)
WHERE state = 'running' AND lease_expires_at IS NOT NULL;

-- name: ExpireExternalExecutions :many
UPDATE task_external_runs SET state = 'stale', ended_at = @at
WHERE id IN (SELECT id FROM task_external_runs WHERE state = 'running'
  AND lease_expires_at <= @at ORDER BY lease_expires_at, id LIMIT 100)
RETURNING *;

-- name: ListTaskExternalRuns :many
SELECT sqlc.embed(e), t.number AS task_number, coalesce(r.name, '') AS runner_name
FROM task_external_runs e JOIN tasks t ON t.id = e.task_id
LEFT JOIN runners r ON r.id = e.runner_id AND r.kind = 'runner'
  AND EXISTS (SELECT 1 FROM runner_pairing p WHERE p.runner_id = r.id
    AND p.credential_digest <> '' AND p.credential_digest = e.runner_enrollment)
WHERE (CAST(@task_id AS TEXT) = '' OR e.task_id = @task_id)
  AND (CAST(@runner_id_filter AS TEXT) = '' OR r.id = @runner_id_filter)
  AND (CAST(@state_filter AS TEXT) = '' OR e.state = @state_filter);
