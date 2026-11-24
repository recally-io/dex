-- name: SupersedeOutboxTopic :execrows
UPDATE outbox_messages
SET status = 'superseded'
WHERE conversation_id = @conversation_id
  AND peer = @peer
  AND topic_key = @topic_key
  AND @topic_key != ''
  AND status = 'pending'
  AND delivered_parts = 0;

-- name: InsertOutboxMessage :execrows
INSERT INTO outbox_messages (
    id, dedup_key, agent_id, conversation_id, source_submission_id,
    kind, peer, channel_context, topic_key, body, reason, status, created_at, delivered_at
) VALUES (
    @id, @dedup_key, @agent_id, @conversation_id, @source_submission_id,
    @kind, @peer, coalesce(nullif(CAST(@channel_context AS TEXT), ''), '{}'),
    @topic_key, @body, @reason, @status, @created_at, @delivered_at
)
ON CONFLICT (dedup_key) DO NOTHING;

-- name: GetOutboxByDedupKey :one
SELECT * FROM outbox_messages WHERE dedup_key = ?;

-- name: GetOutboxMessage :one
SELECT * FROM outbox_messages WHERE id = ?;

-- name: BindOutboxFilesToOutbox :execrows
UPDATE outbox_files
SET outbox_id = @outbox_id
WHERE submission_id = @submission_id AND outbox_id IS NULL;

-- name: QueueOutboxFile :execrows
INSERT INTO outbox_files (id, agent_id, operation_key, submission_id, name, mime_type, body)
SELECT @id, c.agent_id, @operation_key, s.id, @name, @mime_type, @body
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
JOIN agents a ON a.id = c.agent_id
WHERE s.id = @submission_id AND s.status = 'pending' AND s.channel_revoked = 0
  AND s.origin LIKE 'telegram:%'
  AND c.agent_id = @agent_id AND c.execution_scope = @execution_scope AND c.execution_id = @execution_id
  AND c.parent_id IS NULL AND a.archived_at IS NULL
  AND (SELECT count(*) FROM outbox_files f WHERE f.submission_id = s.id AND f.outbox_id IS NULL) < 10
ON CONFLICT DO NOTHING;

-- name: GetOutboxFile :one
SELECT body FROM outbox_files WHERE operation_key = ? AND outbox_id = ?;

-- name: GetOutboxFileBySubmission :one
SELECT body FROM outbox_files WHERE operation_key = ? AND submission_id = ?;

-- name: ListOutboxFiles :many
SELECT operation_key, name, mime_type, length(body) AS size FROM outbox_files
WHERE outbox_id = ? ORDER BY rowid;

-- name: ListPendingOutboxMessages :many
SELECT o.id, o.dedup_key, o.agent_id, o.conversation_id, o.source_submission_id,
       o.kind, o.peer, o.channel_context, o.topic_key, o.body, o.reason,
       o.rendered_parts, o.delivered_parts, o.group_fallback, o.group_cursor, o.remote_ids,
       o.status, o.retry_at, o.error, o.created_at
FROM outbox_messages o
LEFT JOIN submissions src ON src.id = o.source_submission_id
WHERE o.status = 'pending'
  AND o.agent_id = @agent_id
  AND o.peer >= @origin_start AND o.peer < @origin_end
  AND (
      o.kind != 'reply'
      OR NOT EXISTS (
          SELECT 1 FROM submissions p JOIN conversations pc ON pc.id = p.conversation_id
          WHERE p.status = 'pending' AND p.origin = o.peer
            AND p.rowid < coalesce(src.rowid, o.rowid)
            AND p.kind = 'message' AND p.delivered_at IS NULL AND p.channel_revoked = 0
            AND pc.agent_id = o.agent_id
      )
  )
ORDER BY coalesce(src.rowid, o.rowid), o.rowid;

-- name: FreezeOutboxMessage :exec
UPDATE outbox_messages SET rendered_parts = ? WHERE id = ? AND rendered_parts = '';

-- name: AdvanceOutboxMessage :exec
UPDATE outbox_messages
SET delivered_parts = delivered_parts + 1,
    remote_ids = @remote_ids,
    retry_at = 0,
    group_fallback = 0,
    group_cursor = 0
WHERE id = @id;

-- name: StartOutboxGroupFallback :exec
UPDATE outbox_messages SET group_fallback = 1
WHERE id = @id AND delivered_parts = @part AND group_cursor = 0;

-- name: AdvanceOutboxGroupMember :exec
UPDATE outbox_messages
SET group_cursor = group_cursor + 1,
    remote_ids = @remote_ids,
    retry_at = 0
WHERE id = @id;

-- name: SetOutboxError :exec
UPDATE outbox_messages SET retry_at = @retry_at, error = @reason WHERE id = @id;

-- name: MarkOutboxDelivered :exec
UPDATE outbox_messages SET status = 'delivered', delivered_at = @delivered_at WHERE id = @id;

-- name: RevokeOutboxBySubmission :exec
UPDATE outbox_messages SET status = 'revoked' WHERE source_submission_id = ? AND status = 'pending';

-- name: ListRecentDeliveredOutbox :many
SELECT id, kind, peer, topic_key, body, reason, delivered_at, created_at
FROM outbox_messages
WHERE conversation_id = @conversation_id
  AND status = 'delivered'
  AND kind != 'reply'
ORDER BY coalesce(delivered_at, created_at) DESC, rowid DESC
LIMIT 5;

-- name: SetSubmissionExitAction :exec
UPDATE submissions
SET exit_action = @exit_action
WHERE id = @id AND status = 'pending';

-- name: HasOutboxForSource :one
SELECT EXISTS (
    SELECT 1 FROM outbox_messages WHERE source_submission_id = ? AND status IN ('pending', 'delivered')
);

-- name: InsertStoppedOutboxForConversation :exec
INSERT INTO outbox_messages (
    id, dedup_key, agent_id, conversation_id, source_submission_id,
    kind, peer, channel_context, topic_key, body, reason, status, created_at
)
SELECT
    s.id,
    'reply:' || s.id,
    @agent_id,
    s.conversation_id,
    s.id,
    'reply',
    s.origin,
    s.channel_context,
    '',
    '',
    'stopped',
    CASE WHEN s.channel_revoked != 0 THEN 'revoked' ELSE 'pending' END,
    @now
FROM submissions s
WHERE s.conversation_id = @chat_id
  AND s.status = 'pending'
  AND s.kind = 'message'
  AND s.origin LIKE '%:%'
ON CONFLICT(dedup_key) DO NOTHING;

-- name: ListOutboxByConversation :many
SELECT id, kind, peer, topic_key, body, reason, status, error, retry_at, created_at, delivered_at
FROM outbox_messages
WHERE conversation_id = @conversation_id
ORDER BY created_at DESC, rowid DESC
LIMIT 30;

-- name: ListTimelineNotifications :many
-- The submission is the trusted origin even when a Web task also has a channel target.
-- rowid is only a live read position; history sends the durable ID as its cursor.
SELECT CAST(o.rowid AS INTEGER) AS position, o.id, o.body, o.created_at
FROM outbox_messages o
JOIN submissions s ON s.id = o.source_submission_id AND s.conversation_id = o.conversation_id
JOIN conversations c ON c.id = o.conversation_id AND c.agent_id = o.agent_id
JOIN agents a ON a.id = c.agent_id
WHERE o.conversation_id = @conversation_id AND o.agent_id = @agent_id
  AND a.archived_at IS NULL AND c.execution_scope = '' AND c.parent_id IS NULL
  AND (c.channel_key = 'main' OR c.channel_key LIKE 'chat:%')
  AND o.kind = 'proactive' AND o.dedup_key LIKE 'tool:%'
  AND s.origin = 'web' AND s.channel_revoked = 0
  AND o.rowid > CAST(@after_position AS INTEGER)
  AND (CAST(@before_id AS TEXT) = '' OR o.rowid < (SELECT CAST(prior.rowid AS INTEGER) FROM outbox_messages prior
      WHERE prior.id = @before_id AND prior.conversation_id = @conversation_id AND prior.agent_id = @agent_id))
ORDER BY o.rowid DESC
LIMIT @page_size;

-- name: SetRequiredChannelReply :exec
UPDATE submissions SET required_reply_id = @outbox_id
WHERE id = @submission_id AND kind = 'message'
  AND (origin LIKE 'telegram:%' OR origin LIKE 'feishu:%');

-- name: MarkOutboxPartSkipped :exec
UPDATE outbox_messages SET skipped_part = 1 WHERE id = ?;

-- name: GetChannelReactionReceipt :one
SELECT s.status, s.reason, s.exit_action, s.required_reply_id,
       coalesce(o.status, '') AS reply_status, coalesce(o.skipped_part, 0) AS skipped_part
FROM submissions s LEFT JOIN outbox_messages o ON o.id = s.required_reply_id
WHERE s.id = ?;
