-- name: GetSubmissionBySource :one
SELECT id FROM submissions WHERE source_key = @source_key;

-- name: CreateChannelCommand :exec
INSERT INTO channel_commands (bot_id, source_key, peer, channel_key, command, channel_context)
VALUES (@bot_id, @source_key, @peer, @channel_key, @command, coalesce(nullif(CAST(@channel_context AS TEXT), ''), '{}'))
ON CONFLICT DO NOTHING;

-- name: CreateFeedbackCommand :exec
-- The existing command receipt is the outbox. Store the reply atomically with its source key.
INSERT INTO channel_commands (bot_id, source_key, peer, channel_key, command, channel_context, completed, answer)
VALUES (@bot_id, @source_key, @peer, 'main', '/feedback', @channel_context, 1, @answer)
ON CONFLICT DO NOTHING;

-- name: GetChannelCommand :one
SELECT * FROM channel_commands WHERE source_key = ?;

-- name: CompleteChannelCommand :exec
UPDATE channel_commands SET completed = 1, answer = ? WHERE source_key = ?;

-- name: SaveChannelCommandAnswer :exec
UPDATE channel_commands SET answer = ? WHERE source_key = ?;

-- name: ListChannelCommands :many
SELECT * FROM channel_commands WHERE bot_id = ? AND completed = 0 AND delivered_at IS NULL;

-- name: ListChannelCommandReplies :many
SELECT c.* FROM channel_commands c
WHERE c.completed = 1 AND c.delivered_at IS NULL
AND c.bot_id = @bot_id AND c.peer LIKE @prefix || '%'
AND NOT EXISTS (SELECT 1 FROM outbox_messages o WHERE o.id = c.source_key)
ORDER BY c.rowid;

-- name: MarkChannelCommandDelivered :exec
UPDATE channel_commands SET delivered_at = @delivered_at,
    answer = CASE WHEN command = '/feedback' THEN '' ELSE answer END
WHERE source_key = @source_key;

-- name: CountChannelPending :one
SELECT count(*) FROM submissions s JOIN sessions c ON c.id = s.session_id
WHERE s.status = 'pending' AND c.bot_id = @bot_id AND s.origin = @peer;

-- name: TelegramCredentialOwner :one
SELECT a.id, a.name FROM bots a JOIN telegram_settings s ON s.bot_id = a.id
WHERE substr(s.token, 1, instr(s.token, ':') - 1) = sqlc.arg(account) AND s.bot_id != sqlc.arg(bot_id)
ORDER BY a.id LIMIT 1;

-- name: WechatCredentialOwner :one
SELECT a.id, a.name FROM bots a JOIN wechat_settings s ON s.bot_id = a.id WHERE s.wechat_bot_id = ?;

-- name: FeishuCredentialOwner :one
SELECT a.id, a.name FROM bots a JOIN feishu_settings s ON s.bot_id = a.id WHERE s.app_id = ?;

-- name: ListBotActivity :many
WITH active AS (
    SELECT c.bot_id, count(*) AS sessions
    FROM sessions c
    WHERE (c.channel_key LIKE 'chat:%' OR c.parent_id IS NOT NULL OR c.channel_key LIKE 'automation:%')
      AND c.channel_key NOT LIKE 'retired-telegram:%'
      AND (c.channel_key NOT LIKE 'chat:%' OR c.parent_id IS NOT NULL OR EXISTS (
          SELECT 1 FROM submissions m WHERE m.session_id = c.id AND m.kind = 'message'
      ))
      AND EXISTS (SELECT 1 FROM submissions s WHERE s.session_id = c.id AND s.status = 'pending')
    GROUP BY c.bot_id
), enabled AS (
    SELECT bot_id, count(*) AS automations FROM automations WHERE enabled = 1 GROUP BY bot_id
)
SELECT sqlc.embed(a), CAST(coalesce(active.sessions, 0) AS INTEGER) AS active_sessions,
    CAST(coalesce(enabled.automations, 0) AS INTEGER) AS enabled_automations,
    CAST(EXISTS (SELECT 1 FROM automations automation
        JOIN automation_runs r ON r.id = (SELECT latest.id FROM automation_runs latest
            WHERE latest.automation_id = automation.id ORDER BY latest.created_at DESC, latest.id DESC LIMIT 1)
        WHERE automation.bot_id = a.id AND automation.enabled = 1 AND r.outcome = 'failed' AND r.status = 'failed') AS INTEGER) AS automation_failed
FROM bots a LEFT JOIN active ON active.bot_id = a.id LEFT JOIN enabled ON enabled.bot_id = a.id
WHERE a.archived_at IS NULL
ORDER BY a.created_at, a.id;
