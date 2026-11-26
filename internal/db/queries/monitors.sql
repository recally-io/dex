-- name: CreateMonitor :execrows
INSERT INTO monitors (
    id, operation_key, agent_id, conversation_id, root_request_id, source_submission_id,
    chain_depth, due_at, time_zone, prompt, state, created_at, updated_at,
    check_command, check_env, interval_seconds, max_runs, expires_at,
    target_execution_id, target_runner_id, target_runner_revision, target_cwd
) VALUES (
    @id, @operation_key, @agent_id, @conversation_id, @root_request_id, @source_submission_id,
    @chain_depth, @due_at, @time_zone, @prompt, 'scheduled', @created_at, @updated_at,
    @check_command, @check_env, @interval_seconds, @max_runs, @expires_at,
    @target_execution_id, @target_runner_id, @target_runner_revision, @target_cwd
)
ON CONFLICT (operation_key) DO NOTHING;

-- name: GetMonitor :one
SELECT * FROM monitors WHERE id = ?;

-- name: GetMonitorByOperationKey :one
SELECT * FROM monitors WHERE operation_key = ?;

-- name: CountScheduledMonitors :one
SELECT count(*) FROM monitors WHERE conversation_id = ? AND state = 'scheduled';

-- name: ListMonitorsByConversation :many
SELECT * FROM monitors
WHERE conversation_id = ?
ORDER BY CASE WHEN state = 'scheduled' THEN 0 ELSE 1 END, due_at ASC, rowid DESC
LIMIT 50;

-- name: ListDueMonitors :many
SELECT * FROM monitors
WHERE state = 'scheduled' AND (running = 1 OR due_at <= @now OR (expires_at > 0 AND expires_at <= @now))
ORDER BY due_at ASC, rowid ASC;

-- name: ClaimMonitor :execrows
UPDATE monitors SET running = 1, runs = runs + 1, updated_at = @now
WHERE id = @id AND state = 'scheduled' AND running = 0 AND runs < max_runs;

-- name: FinishMonitor :execrows
UPDATE monitors
SET state = @state, fire_submission_id = coalesce(sqlc.narg(fire_submission_id), fire_submission_id),
    due_at = @due_at, runs = @new_runs, output_hash = @output_hash, outcome = @outcome, running = 0, updated_at = @updated_at
WHERE id = @id AND state = 'scheduled' AND runs = @runs;

-- name: CancelMonitor :execrows
UPDATE monitors
SET state = 'cancelled', outcome = 'cancelled', running = 0, updated_at = @updated_at
WHERE id = @id AND conversation_id = @conversation_id AND state = 'scheduled';

-- name: CancelMonitorsByConversation :execrows
UPDATE monitors
SET state = 'cancelled', outcome = 'session stopped or cleared', running = 0, updated_at = @updated_at
WHERE conversation_id = @conversation_id AND state = 'scheduled';

-- name: CreateMonitorSubmission :execrows
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, producer, reply_channel, reply_peer, kind, status, created_at, channel_context, when_busy)
SELECT @id, t.conversation_id, @content, 'null', @id, root.origin, 'scheduler', '', '', 'task_result', 'pending', @created_at, root.channel_context, 'followUp'
FROM monitors t JOIN submissions root ON root.id = t.root_request_id
WHERE t.id = @monitor_id
ON CONFLICT DO NOTHING;

-- name: ListPendingMonitorEvents :many
SELECT s.id FROM monitors m JOIN submissions s ON s.conversation_id = m.conversation_id
WHERE m.id = ? AND s.kind = 'task_result' AND s.source_key = s.id
  AND (s.status = 'pending' OR (s.status = 'unanswered' AND s.reason = 'aborted'))
  AND (s.id = m.fire_submission_id OR substr(s.id, 1, length(m.id) + 9) = 'monitor:' || m.id || ':')
ORDER BY s.rowid;
