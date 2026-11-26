-- name: SupersedeOutboxTopic :execrows
UPDATE outbox_messages
SET status = 'superseded'
WHERE conversation_id = @conversation_id AND peer = @peer
  AND topic_key = @topic_key AND @topic_key != ''
  AND status = 'pending' AND rendered_message = '';

-- name: InsertOutboxMessage :execrows
INSERT INTO outbox_messages (
    id, event_id, agent_id, conversation_id, source_submission_id,
    kind, peer, channel_context, topic_key, body, reason, status, created_at, delivered_at, rendered_message
) VALUES (
    @id, @event_id, @agent_id, @conversation_id, @source_submission_id,
    @kind, @peer, coalesce(nullif(CAST(@channel_context AS TEXT), ''), '{}'),
    @topic_key, @body, @reason, @status, @created_at, @delivered_at, @rendered_message
)
ON CONFLICT (event_id, channel, target) DO NOTHING;

-- name: GetOutboxByEvent :one
SELECT * FROM outbox_messages WHERE event_id = @event_id AND peer = @peer;

-- name: FindNotificationReceipt :one
-- A tool invocation chooses its destination once, even if settings change before replay.
SELECT o.id FROM outbox_messages o WHERE o.event_id = @event_id AND o.kind = 'proactive'
UNION ALL
SELECT n.id FROM web_notifications n WHERE n.event_id = @event_id
LIMIT 1;

-- name: GetOutboxMessage :one
SELECT * FROM outbox_messages WHERE id = ?;

-- name: GetPendingAutomationOutboxPeer :one
-- The event ID stays the same across upgrades and retries.
SELECT peer FROM outbox_messages WHERE event_id = ? AND kind = 'automation' AND status = 'pending';

-- name: CreateFileDeliveries :exec
INSERT INTO outbox_messages (id, event_id, agent_id, conversation_id, source_submission_id,
    kind, peer, channel_context, body, created_at)
SELECT 'file:' || f.id, 'file:' || f.agent_id || ':' || f.operation_key,
    o.agent_id, o.conversation_id, o.source_submission_id,
    'file', o.peer, o.channel_context, f.name, o.created_at
FROM outbox_files f JOIN outbox_messages o ON o.id = @outbox_id
WHERE f.submission_id = @submission_id AND f.outbox_id IS NULL AND o.status = 'pending'
ORDER BY f.rowid
ON CONFLICT (event_id, channel, target) DO NOTHING;

-- name: BindFileDeliveries :exec
UPDATE outbox_files SET outbox_id = 'file:' || id
WHERE submission_id = @submission_id AND outbox_id IS NULL
  AND EXISTS (SELECT 1 FROM outbox_messages o WHERE o.id = 'file:' || outbox_files.id AND o.status = 'pending');

-- name: DeleteUnboundOutboxFiles :exec
DELETE FROM outbox_files WHERE submission_id = ? AND outbox_id IS NULL;

-- name: QueueOutboxFile :execrows
INSERT INTO outbox_files (id, agent_id, operation_key, submission_id, name, mime_type, body)
SELECT @id, c.agent_id, @operation_key, s.id, @name, @mime_type, @body
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
JOIN agents a ON a.id = c.agent_id
WHERE s.id = @submission_id AND s.status = 'pending' AND s.channel_revoked = 0
  AND s.origin LIKE 'telegram:%'
  AND c.agent_id = @agent_id AND c.channel_key NOT LIKE 'retired-telegram:%' AND c.execution_id = @execution_id
  AND c.parent_id IS NULL AND a.archived_at IS NULL
  AND (SELECT count(*) FROM outbox_files f WHERE f.submission_id = s.id AND f.outbox_id IS NULL) < 10
ON CONFLICT DO NOTHING;

-- name: GetOutboxFileBySubmission :one
SELECT body FROM outbox_files WHERE operation_key = ? AND submission_id = ?;

-- name: GetOutboxFile :one
SELECT operation_key, name, mime_type, length(body) AS size, body FROM outbox_files WHERE outbox_id = ?;

-- name: ListPendingOutboxMessages :many
-- Pending Outbox rows are sent in input order. An earlier input without a terminal
-- reply can also hold the queue before it has an Outbox row.
SELECT o.*
FROM outbox_messages o
JOIN conversations c ON c.id = o.conversation_id
LEFT JOIN submissions src ON src.id = o.source_submission_id
WHERE o.status = 'pending' AND c.channel_key NOT LIKE 'retired-telegram:%'
  AND o.agent_id = @agent_id AND o.channel = @channel
  AND (
      o.kind != 'reply'
      OR NOT EXISTS (
          SELECT 1 FROM submissions p JOIN conversations pc ON pc.id = p.conversation_id
          WHERE p.status = 'pending' AND p.origin = o.peer
            AND p.rowid < coalesce(src.rowid, o.rowid)
            AND p.kind = 'message' AND p.channel_revoked = 0
            AND pc.agent_id = o.agent_id
            AND NOT EXISTS (
                SELECT 1 FROM outbox_messages prior
                WHERE prior.source_submission_id = p.id
                  AND prior.kind IN ('reply', 'question') AND prior.status IN ('sent', 'failed')
            )
      )
  )
ORDER BY (o.kind IN ('control', 'feedback')) DESC, coalesce(src.rowid, o.rowid), o.rowid;

-- name: FreezeOutboxMessage :exec
UPDATE outbox_messages SET rendered_message = @rendered_message
WHERE id = @id AND status = 'pending' AND rendered_message = '';

-- name: SetOutboxError :exec
UPDATE outbox_messages SET retry_at = @retry_at, error = @reason, attempts = attempts + 1
WHERE id = @id AND status = 'pending';

-- name: FailOutboxMessage :exec
UPDATE outbox_messages SET status = 'failed', retry_at = 0, error = @reason
WHERE id = @id AND status = 'pending';

-- name: MarkOutboxSent :exec
UPDATE outbox_messages SET status = 'sent', delivered_at = @delivered_at, remote_ids = @remote_ids, retry_at = 0, error = ''
WHERE id = @id AND status IN ('pending', 'failed');

-- name: ListRecentDeliveredOutbox :many
SELECT id, kind, peer, topic_key, body, reason, delivered_at, created_at
FROM outbox_messages
WHERE conversation_id = @conversation_id AND status = 'sent' AND kind NOT IN ('reply', 'control', 'feedback', 'file')
ORDER BY coalesce(delivered_at, created_at) DESC, rowid DESC
LIMIT 5;

-- name: SetSubmissionExitAction :exec
UPDATE submissions SET exit_action = @exit_action WHERE id = @id AND status = 'pending';

-- name: InsertStoppedOutboxForConversation :exec
INSERT INTO outbox_messages (
    id, event_id, agent_id, conversation_id, source_submission_id,
    kind, peer, channel_context, reason, status, created_at
)
SELECT s.id, 'reply:' || @agent_id || ':' || s.id, @agent_id, s.conversation_id, s.id,
    'reply', s.origin, s.channel_context, 'stopped', 'pending', @now
FROM submissions s
WHERE s.conversation_id = @chat_id AND s.status = 'pending' AND s.kind = 'message'
  AND (s.origin LIKE 'telegram:%' OR s.origin LIKE 'feishu:%' OR s.origin LIKE 'wechat:%')
  AND s.channel_revoked = 0
ON CONFLICT DO NOTHING;

-- name: ListOutboxByConversation :many
SELECT id, kind, peer, topic_key, body, reason, status, error, retry_at, created_at, delivered_at
FROM outbox_messages
WHERE conversation_id = @conversation_id AND (kind NOT IN ('control', 'feedback') OR status = 'failed')
ORDER BY created_at DESC, rowid DESC
LIMIT 30;

-- name: InsertWebNotification :exec
INSERT INTO web_notifications (id, event_id, agent_id, conversation_id, source_submission_id, body, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (event_id) DO NOTHING;

-- name: ListTimelineNotifications :many
-- The submission is the trusted origin even when a Web task also has a channel target.
-- rowid is only a live read position; history sends the durable ID as its cursor.
SELECT CAST(o.rowid AS INTEGER) AS position, o.id, o.body, o.created_at
FROM web_notifications o
JOIN submissions s ON s.id = o.source_submission_id AND s.conversation_id = o.conversation_id
JOIN conversations c ON c.id = o.conversation_id AND c.agent_id = o.agent_id
JOIN agents a ON a.id = c.agent_id
WHERE o.conversation_id = @conversation_id AND o.agent_id = @agent_id
  AND a.archived_at IS NULL AND c.channel_key NOT LIKE 'retired-telegram:%' AND c.parent_id IS NULL
  AND (c.channel_key = 'main' OR c.channel_key LIKE 'chat:%')
  AND (s.origin = 'web' OR s.producer <> 'user')
  AND o.rowid > CAST(@after_position AS INTEGER)
  AND (CAST(@before_id AS TEXT) = '' OR o.rowid < (SELECT CAST(prior.rowid AS INTEGER) FROM web_notifications prior
      WHERE prior.id = @before_id AND prior.conversation_id = @conversation_id AND prior.agent_id = @agent_id))
ORDER BY o.rowid DESC
LIMIT @page_size;

-- name: SetRequiredChannelReply :exec
UPDATE submissions SET required_reply_id = @outbox_id
WHERE id = @submission_id AND kind = 'message'
  AND (origin LIKE 'telegram:%' OR origin LIKE 'feishu:%');

-- name: GetChannelReactionReceipt :one
SELECT s.status, s.stop_requested, s.reason, s.exit_action, s.required_reply_id,
       coalesce(o.status, '') AS reply_status
FROM submissions s LEFT JOIN outbox_messages o ON o.id = s.required_reply_id
WHERE s.id = ?;
