-- name: CreateChannelFollowUp :execrows
INSERT INTO channel_follow_ups (delegation_id, result_id, conversation_id)
SELECT @delegation_id, @result_id, @conversation_id
FROM authorized_channel_follow_up_sources s WHERE s.id = @source_id;

-- name: GetChannelFollowUpByInput :one
SELECT * FROM channel_follow_ups WHERE result_id = ?;

-- name: GetChannelFollowUpByDelegation :one
SELECT * FROM channel_follow_ups WHERE delegation_id = ?;

-- name: SetOpenChannelFollowUpState :exec
UPDATE channel_follow_ups SET state = @state WHERE result_id = @result_id AND state = 'open';

-- name: CancelChannelFollowUps :exec
UPDATE channel_follow_ups SET state = 'cancelled' WHERE delegation_id IN (
    SELECT p.delegation_id FROM pending_channel_follow_ups p WHERE p.conversation_id = @chat_id OR p.child_id = @chat_id
);
