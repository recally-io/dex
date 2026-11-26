-- name: CreateTelegramCallback :exec
INSERT INTO telegram_callbacks (id, agent_id, source_id, ordinal, peer, channel_context,
    conversation_id, execution_id, context_revision, label, object_id, action, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id, ordinal) DO NOTHING;

-- name: GetTelegramCallbackSlot :one
SELECT * FROM telegram_callbacks WHERE source_id = ? AND ordinal = ?;

-- name: GetTelegramCallback :one
SELECT * FROM telegram_callbacks WHERE id = ?;

-- name: BindTelegramCallbackMessage :exec
UPDATE telegram_callbacks SET message_id = @message_id
WHERE source_id = @source_id AND message_id = '';

-- name: ConsumeTelegramCallback :execrows
UPDATE telegram_callbacks SET consumed_at = @now
WHERE id = @id AND consumed_at IS NULL AND expires_at > @now AND message_id <> '';

-- name: TelegramContextRevision :one
SELECT CAST(coalesce((SELECT id FROM submissions
WHERE conversation_id = @conversation_id AND kind = 'context_reset'
ORDER BY rowid DESC LIMIT 1), '') AS TEXT);

-- name: GetChildWorkflowSource :one
SELECT s.* FROM submissions s
WHERE s.conversation_id = @child_id AND s.parent_request_id IS NOT NULL
ORDER BY s.rowid LIMIT 1;
