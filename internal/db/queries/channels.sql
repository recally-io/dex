-- name: GetSubmissionBySource :one
SELECT s.id FROM submissions s WHERE s.source_key = @source_key
UNION ALL
SELECT s.id FROM telegram_album_members m JOIN submissions s ON s.source_key = m.batch_source
WHERE m.source_key = @source_key
LIMIT 1;

-- name: CreateChannelCommand :exec
INSERT INTO channel_commands (agent_id, source_key, peer, channel_key, command, channel_context)
VALUES (@agent_id, @source_key, @peer, @channel_key, @command, coalesce(nullif(CAST(@channel_context AS TEXT), ''), '{}'))
ON CONFLICT DO NOTHING;

-- name: CreateFeedbackCommand :exec
-- The existing command receipt is the outbox. Store the reply atomically with its source key.
INSERT INTO channel_commands (agent_id, source_key, peer, channel_key, command, channel_context, completed, answer)
VALUES (@agent_id, @source_key, @peer, 'main', '/feedback', @channel_context, 1, @answer)
ON CONFLICT DO NOTHING;

-- name: GetChannelCommand :one
SELECT * FROM channel_commands WHERE source_key = ?;

-- name: CompleteChannelCommand :exec
UPDATE channel_commands SET completed = 1, answer = ? WHERE source_key = ?;

-- name: SaveChannelCommandAnswer :exec
UPDATE channel_commands SET answer = ? WHERE source_key = ?;

-- name: ListChannelCommands :many
SELECT * FROM channel_commands WHERE agent_id = ? AND completed = 0 AND delivered_at IS NULL;

-- name: ListChannelReplies :many
-- A reply waits for every earlier unrevoked pending message of the same agent and origin, so a
-- later answer cannot pass an earlier one whose outcome is not written yet. Settled, delivered,
-- revoked and non-message rows do not hold the barrier. Its origin lookup uses submissions_channel_pending.
SELECT s.id, s.origin, s.status, s.answer, s.reason, s.delivered_parts,
       s.rendered_reply, s.channel_context, s.channel_remote_ids, s.channel_revoked, s.channel_retry_at,
       s.channel_group_fallback, s.channel_group_cursor
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.status != 'pending' AND s.delivered_at IS NULL
AND (s.kind = 'message' OR (s.kind = 'task_result' AND s.status = 'done'))
AND c.agent_id = @agent_id AND s.origin >= @origin_start AND s.origin < @origin_end
AND NOT EXISTS (
    SELECT 1 FROM submissions p JOIN conversations pc ON pc.id = p.conversation_id
    WHERE p.status = 'pending' AND p.origin = s.origin AND p.rowid < s.rowid
    AND p.kind = 'message' AND p.delivered_at IS NULL AND p.channel_revoked = 0 AND pc.agent_id = c.agent_id
)
ORDER BY s.rowid;

-- name: RevokeChannelSubmission :exec
UPDATE submissions SET channel_revoked = 1 WHERE id = ?;

-- name: MarkChannelReplyDelivered :exec
UPDATE submissions SET delivered_at = ? WHERE id = ?;

-- name: ListChannelCommandReplies :many
SELECT c.* FROM channel_commands c
WHERE c.completed = 1 AND c.delivered_at IS NULL
AND c.agent_id = @agent_id AND c.peer LIKE @prefix || '%'
AND NOT EXISTS (SELECT 1 FROM outbox_messages o WHERE o.id = c.source_key)
ORDER BY c.rowid;

-- name: MarkChannelCommandDelivered :exec
UPDATE channel_commands SET delivered_at = @delivered_at,
    answer = CASE WHEN command = '/feedback' THEN '' ELSE answer END
WHERE source_key = @source_key;

-- name: CountChannelPending :one
SELECT count(*) FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending' AND c.agent_id = @agent_id AND s.origin = @peer;

-- name: TelegramCredentialOwner :one
SELECT a.id, a.name FROM agents a JOIN telegram_settings s ON s.agent_id = a.id
WHERE substr(s.token, 1, instr(s.token, ':') - 1) = sqlc.arg(account) AND s.agent_id != sqlc.arg(agent_id)
ORDER BY a.id LIMIT 1;

-- name: WechatCredentialOwner :one
SELECT a.id, a.name FROM agents a JOIN wechat_settings s ON s.agent_id = a.id WHERE s.bot_id = ?;

-- name: FeishuCredentialOwner :one
SELECT a.id, a.name FROM agents a JOIN feishu_settings s ON s.agent_id = a.id WHERE s.app_id = ?;

-- name: ListAgentActivity :many
WITH active AS (
    SELECT c.agent_id, count(*) AS sessions
    FROM conversations c
    WHERE (c.channel_key LIKE 'chat:%' OR c.channel_key LIKE 'task:%' OR c.channel_key LIKE 'automation:%')
      AND (c.channel_key NOT LIKE 'chat:%' OR EXISTS (
          SELECT 1 FROM submissions m WHERE m.conversation_id = c.id AND m.kind = 'message'
      ))
      AND EXISTS (SELECT 1 FROM submissions s WHERE s.conversation_id = c.id AND s.status = 'pending')
    GROUP BY c.agent_id
), enabled AS (
    SELECT agent_id, count(*) AS automations FROM automations WHERE enabled = 1 GROUP BY agent_id
)
SELECT sqlc.embed(a), CAST(coalesce(active.sessions, 0) AS INTEGER) AS active_sessions,
    CAST(coalesce(enabled.automations, 0) AS INTEGER) AS enabled_automations,
    CAST(EXISTS (SELECT 1 FROM automations routine
        JOIN automation_runs r ON r.id = (SELECT latest.id FROM automation_runs latest
            WHERE latest.automation_id = routine.id ORDER BY latest.created_at DESC, latest.id DESC LIMIT 1)
        WHERE routine.agent_id = a.id AND routine.enabled = 1 AND r.outcome = 'failed' AND r.status = 'failed') AS INTEGER) AS routine_failed
FROM agents a LEFT JOIN active ON active.agent_id = a.id LEFT JOIN enabled ON enabled.agent_id = a.id
WHERE a.archived_at IS NULL
ORDER BY a.created_at, a.id;
