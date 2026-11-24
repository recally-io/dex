-- name: CreateChannelFollowUp :exec
INSERT INTO channel_follow_ups (delegation_id, result_id, conversation_id, child_id, parent_request_id, parent_source_key)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetChannelFollowUpByInput :one
SELECT * FROM channel_follow_ups WHERE result_id = ?;

-- name: GetChannelFollowUpByDelegation :one
SELECT * FROM channel_follow_ups WHERE delegation_id = ?;

-- name: BindChannelFollowUpActivation :execrows
UPDATE channel_follow_ups SET native_activation_id = @native_activation_id
WHERE delegation_id = @delegation_id AND (native_activation_id IS NULL OR native_activation_id = @native_activation_id);

-- name: StageChannelFollowUpReport :exec
UPDATE channel_follow_ups SET staged_report = @staged_report WHERE delegation_id = @delegation_id AND staged_report IS NULL;

-- name: ConsumeChannelFollowUpReport :exec
UPDATE channel_follow_ups SET staged_report = NULL WHERE delegation_id = ?;

-- name: GetFollowUpDispatchState :one
SELECT o.status, EXISTS (
    SELECT 1 FROM channel_follow_ups f
    JOIN submissions original ON original.id = f.parent_request_id
    JOIN submissions result ON result.id = f.result_id
    WHERE o.source_submission_id = result.id AND o.dedup_key = 'follow-up-final:' || f.delegation_id
      AND f.state = 'notified'
      AND original.conversation_id = f.conversation_id AND result.conversation_id = f.conversation_id
      AND original.source_key = f.parent_source_key AND original.channel_revoked = 0 AND result.channel_revoked = 0
      AND original.origin = o.peer AND original.channel_context = o.channel_context
) AS authorized
FROM outbox_messages o WHERE o.id = ?;

-- name: SetOpenChannelFollowUpState :exec
UPDATE channel_follow_ups SET state = @state WHERE result_id = @result_id AND state = 'open';

-- name: CancelChannelFollowUps :many
UPDATE channel_follow_ups SET state = 'cancelled'
WHERE (channel_follow_ups.conversation_id = @chat_id OR channel_follow_ups.delegation_id IN (
    SELECT s.id FROM submissions s WHERE s.conversation_id = @chat_id
)) AND (state IN ('open', 'notified') OR EXISTS (
    SELECT 1 FROM outbox_messages o WHERE o.source_submission_id = channel_follow_ups.result_id AND o.status = 'pending'
))
RETURNING result_id;
