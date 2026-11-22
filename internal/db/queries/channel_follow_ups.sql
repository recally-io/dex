-- name: CreateChannelFollowUp :exec
INSERT INTO channel_follow_ups (delegation_id, source_id, conversation_id, child_id, agent_id, peer, account_id, sender_id, channel_context, result_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetChannelFollowUpByInput :one
SELECT * FROM channel_follow_ups WHERE result_id = @id;

-- name: GetChannelFollowUpByDelegation :one
SELECT * FROM channel_follow_ups WHERE delegation_id = ?;

-- name: SetOpenChannelFollowUpState :exec
UPDATE channel_follow_ups SET state = @state WHERE delegation_id = @delegation_id AND state = 'open';

-- name: CancelChannelFollowUps :exec
UPDATE channel_follow_ups SET state = 'cancelled'
WHERE (channel_follow_ups.conversation_id = @chat_id OR channel_follow_ups.child_id = @chat_id)
  AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
      AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = channel_follow_ups.result_id AND status = 'pending')))
  AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || channel_follow_ups.delegation_id AND outbox_messages.status = 'delivered');
