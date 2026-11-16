-- name: QueueChannelReplyFile :execrows
INSERT INTO channel_reply_files (operation_key, submission_id, name, mime_type, body)
SELECT @operation_key, s.id, @name, @mime_type, @body
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
JOIN agents a ON a.id = c.agent_id
WHERE s.id = @submission_id AND s.status = 'pending' AND s.channel_revoked = 0
  AND s.origin LIKE 'telegram:%'
  AND c.agent_id = @agent_id AND c.execution_id = @execution_id
  AND c.parent_id IS NULL AND a.archived_at IS NULL
  AND (SELECT count(*) FROM channel_reply_files f WHERE f.submission_id = s.id) < 10
ON CONFLICT DO NOTHING;

-- name: GetChannelReplyFile :one
SELECT body FROM channel_reply_files WHERE operation_key = ? AND submission_id = ?;

-- name: ListChannelReplyFiles :many
SELECT operation_key, name, mime_type, length(body) AS size FROM channel_reply_files
WHERE submission_id = ? ORDER BY rowid;

-- name: FreezeChannelReply :exec
UPDATE submissions SET rendered_reply = ? WHERE id = ? AND rendered_reply = '';

-- name: SetChannelReplyError :exec
UPDATE submissions SET channel_retry_at = @retry_at, channel_delivery_error = @reason WHERE id = @id;
