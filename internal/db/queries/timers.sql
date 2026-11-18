-- name: CreateFollowupTimer :execrows
INSERT INTO followup_timers (
    id, operation_key, agent_id, conversation_id, root_request_id, source_submission_id,
    chain_depth, due_at, time_zone, prompt, state, created_at, updated_at
) VALUES (
    @id, @operation_key, @agent_id, @conversation_id, @root_request_id, @source_submission_id,
    @chain_depth, @due_at, @time_zone, @prompt, 'scheduled', @created_at, @updated_at
)
ON CONFLICT (operation_key) DO NOTHING;

-- name: GetFollowupTimer :one
SELECT * FROM followup_timers WHERE id = ?;

-- name: GetFollowupTimerByOperationKey :one
SELECT * FROM followup_timers WHERE operation_key = ?;

-- name: GetFollowupTimerByFireSubmission :one
SELECT * FROM followup_timers WHERE fire_submission_id = ?;

-- name: CountScheduledFollowupTimers :one
SELECT count(*) FROM followup_timers WHERE conversation_id = ? AND state = 'scheduled';

-- name: LatestUserSubmissionInConversation :one
SELECT id FROM submissions
WHERE conversation_id = ? AND kind = 'message' AND origin != 'task'
ORDER BY rowid DESC
LIMIT 1;

-- name: ListFollowupTimersByConversation :many
SELECT * FROM followup_timers
WHERE conversation_id = ?
ORDER BY CASE WHEN state = 'scheduled' THEN 0 ELSE 1 END, due_at ASC, rowid DESC
LIMIT 20;

-- name: ListDueFollowupTimers :many
SELECT * FROM followup_timers
WHERE state = 'scheduled' AND due_at <= ?
ORDER BY due_at ASC, rowid ASC;

-- name: MarkFollowupTimerFired :execrows
UPDATE followup_timers
SET state = @state, fire_submission_id = @fire_submission_id, updated_at = @updated_at
WHERE id = @id AND state = 'scheduled';

-- name: CancelFollowupTimer :execrows
UPDATE followup_timers
SET state = 'cancelled', updated_at = @updated_at
WHERE id = @id AND conversation_id = @conversation_id AND state = 'scheduled';

-- name: CancelFollowupTimersByConversation :execrows
UPDATE followup_timers
SET state = 'cancelled', updated_at = @updated_at
WHERE conversation_id = @conversation_id AND state = 'scheduled';

-- name: CreateTimerSubmission :execrows
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at, channel_context)
SELECT @id, @conversation_id, @content, 'null', @id, coalesce(root.origin, 'web'), 'task_result', 'pending', @created_at, coalesce(root.channel_context, '{}')
FROM followup_timers t
LEFT JOIN submissions root ON root.id = t.root_request_id
WHERE t.id = @timer_id
ON CONFLICT DO NOTHING;
