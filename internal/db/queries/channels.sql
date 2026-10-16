-- name: GetSubmissionBySource :one
SELECT id FROM submissions WHERE source_key = ?;

-- name: CreateChannelCommand :exec
INSERT INTO channel_commands (source_key, peer, channel_key, command) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetChannelCommand :one
SELECT * FROM channel_commands WHERE source_key = ?;

-- name: CompleteChannelCommand :exec
UPDATE channel_commands SET completed = 1, answer = ? WHERE source_key = ?;

-- name: ListChannelCommands :many
SELECT * FROM channel_commands WHERE completed = 0;

-- name: ListChannelReplies :many
SELECT s.id, s.origin, s.status, s.answer, s.reason, s.delivered_parts
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.status != 'pending' AND s.delivered_at IS NULL
AND (s.kind = 'message' OR (s.kind = 'task_result' AND s.status = 'done'))
AND c.agent_id = @agent_id AND s.origin LIKE @prefix || '%' ORDER BY s.rowid;

-- name: MarkChannelReplyDelivered :exec
UPDATE submissions SET delivered_at = ? WHERE id = ?;

-- name: ListChannelCommandReplies :many
SELECT * FROM channel_commands WHERE completed = 1 AND delivered_at IS NULL
AND peer LIKE @prefix || '%' ORDER BY rowid;

-- name: MarkChannelCommandDelivered :exec
UPDATE channel_commands SET delivered_at = ? WHERE source_key = ?;

-- name: CountChannelPending :one
SELECT count(*) FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending' AND c.agent_id = @agent_id AND s.origin = @peer;

-- name: AdvanceChannelReply :exec
UPDATE submissions SET delivered_parts = delivered_parts + 1 WHERE id = ?;
