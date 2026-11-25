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
