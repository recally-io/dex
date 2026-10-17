-- name: GetSubmissionBySource :one
SELECT id FROM submissions WHERE source_key = ?;

-- name: CreateChannelCommand :exec
INSERT INTO channel_commands (agent_id, source_key, peer, channel_key, command) VALUES (?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetChannelCommand :one
SELECT * FROM channel_commands WHERE source_key = ?;

-- name: CompleteChannelCommand :exec
UPDATE channel_commands SET completed = 1, answer = ? WHERE source_key = ?;

-- name: SaveChannelCommandAnswer :exec
UPDATE channel_commands SET answer = ? WHERE source_key = ?;

-- name: ListChannelCommands :many
SELECT * FROM channel_commands WHERE agent_id = ? AND completed = 0;

-- name: ListChannelReplies :many
SELECT s.id, s.origin, s.status, s.answer, s.reason, s.delivered_parts
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.status != 'pending' AND s.delivered_at IS NULL
AND (s.kind = 'message' OR (s.kind = 'task_result' AND s.status = 'done'))
AND c.agent_id = @agent_id AND s.origin >= @origin_start AND s.origin < @origin_end
ORDER BY s.rowid;

-- name: MarkChannelReplyDelivered :exec
UPDATE submissions SET delivered_at = ? WHERE id = ?;

-- name: ListChannelCommandReplies :many
SELECT * FROM channel_commands WHERE completed = 1 AND delivered_at IS NULL
AND agent_id = @agent_id AND peer LIKE @prefix || '%' ORDER BY rowid;

-- name: MarkChannelCommandDelivered :exec
UPDATE channel_commands SET delivered_at = ? WHERE source_key = ?;

-- name: CountChannelPending :one
SELECT count(*) FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending' AND c.agent_id = @agent_id AND s.origin = @peer;

-- name: AdvanceChannelReply :exec
UPDATE submissions SET delivered_parts = delivered_parts + 1 WHERE id = ?;

-- name: TelegramCredentialOwner :one
SELECT a.id, a.name FROM agents a JOIN telegram_settings s ON s.agent_id = a.id WHERE s.token = ?;

-- name: WechatCredentialOwner :one
SELECT a.id, a.name FROM agents a JOIN wechat_settings s ON s.agent_id = a.id WHERE s.bot_id = ?;

-- name: FeishuCredentialOwner :one
SELECT a.id, a.name FROM agents a JOIN feishu_settings s ON s.agent_id = a.id WHERE s.app_id = ?;

-- name: ListAgentActivity :many
WITH active AS (
    SELECT c.agent_id, count(*) AS sessions
    FROM conversations c LEFT JOIN tasks t ON t.conversation_id = c.id
    WHERE (c.channel_key LIKE 'chat:%' OR c.channel_key LIKE 'task:%' OR c.channel_key LIKE 'automation:%')
      AND (c.channel_key NOT LIKE 'chat:%' OR EXISTS (
          SELECT 1 FROM submissions m WHERE m.conversation_id = c.id AND m.kind = 'message'
      ))
      AND CASE WHEN t.id IS NOT NULL THEN t.status IN ('queued', 'running')
          ELSE EXISTS (SELECT 1 FROM submissions s WHERE s.conversation_id = c.id AND s.status = 'pending') END
    GROUP BY c.agent_id
), enabled AS (
    SELECT agent_id, count(*) AS automations FROM automations WHERE enabled = 1 GROUP BY agent_id
)
SELECT sqlc.embed(a), CAST(coalesce(active.sessions, 0) AS INTEGER) AS active_sessions,
    CAST(coalesce(enabled.automations, 0) AS INTEGER) AS enabled_automations
FROM agents a LEFT JOIN active ON active.agent_id = a.id LEFT JOIN enabled ON enabled.agent_id = a.id
ORDER BY a.created_at, a.id;
