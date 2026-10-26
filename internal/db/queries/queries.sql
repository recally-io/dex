-- name: CreateAgentIfMissing :exec
INSERT INTO agents (id, name, model_provider, model_id, created_at)
VALUES (?, ?, '', '', ?)
ON CONFLICT (id) DO NOTHING;

-- name: UpdateAgentSettings :exec
UPDATE agents SET name = ?, model_provider = ?, model_id = ?, thinking_level = ?, instructions = ? WHERE id = ?;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: ListAgents :many
SELECT * FROM agents ORDER BY created_at, id;

-- name: CreateAgent :one
INSERT INTO agents (id, name, model_provider, model_id, thinking_level, instructions, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: EnsureConversation :one
-- The title and the settings are set only when the conversation is created.
INSERT INTO conversations (agent_id, channel_key, title, model_provider, model_id, thinking_level, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id, channel_key) DO UPDATE SET agent_id = excluded.agent_id
RETURNING *;

-- name: EnsureEmptyChat :one
-- One write statement serializes concurrent opens and first-message admission.
INSERT INTO conversations (agent_id, channel_key, title, created_at)
VALUES (
    @agent_id,
    coalesce((
        SELECT c.channel_key FROM conversations c
        WHERE c.agent_id = @agent_id AND c.channel_key LIKE 'chat:%' AND c.archived_at IS NULL
            AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.conversation_id = c.id AND s.kind = 'message')
        ORDER BY c.created_at DESC, c.id DESC LIMIT 1
    ), @channel_key),
    '', @created_at
)
ON CONFLICT (agent_id, channel_key) DO UPDATE SET agent_id = excluded.agent_id
RETURNING *;

-- name: ArchiveChat :exec
-- Keeps the first archive time when the chat is archived already.
UPDATE conversations SET archived_at = coalesce(archived_at, @archived_at)
WHERE id = @id AND agent_id = @agent_id AND channel_key LIKE 'chat:%';

-- name: UnarchiveChat :exec
UPDATE conversations SET archived_at = NULL
WHERE id = @id AND agent_id = @agent_id AND channel_key LIKE 'chat:%';

-- name: SetFirstChatTitle :exec
UPDATE conversations SET title = ? WHERE id = ? AND title = '';

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = ?;

-- name: GetConversationByKey :one
SELECT * FROM conversations WHERE agent_id = ? AND channel_key = ?;

-- name: SetConversationSettings :exec
UPDATE conversations SET model_provider = ?, model_id = ?, thinking_level = ?
WHERE id = ? AND agent_id = ?;

-- name: SetExecution :exec
UPDATE conversations SET execution_id = ? WHERE id = ?;

-- name: CreateSubmission :execrows
-- A message whose ID or source key exists already is not stored again.
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)
ON CONFLICT DO NOTHING;

-- name: GetSubmission :one
SELECT * FROM submissions WHERE id = ?;

-- name: SettleSubmission :execrows
UPDATE submissions SET status = ?, answer = ?, reason = ?, settled_at = ?
WHERE id = ? AND status = 'pending';

-- name: GetSubmissionReply :one
SELECT
    s.origin,
    c.id AS conversation_id
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.id = ?;

-- name: ListPendingSubmissions :many
-- SQLite assigns rowids under the writer lock. Delivery follows admission, not wall time.
SELECT
    s.id,
    s.content,
    s.attachments,
    s.kind,
    s.origin,
    s.admitted_at,
    s.conversation_id,
    c.agent_id,
    c.channel_key
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending'
ORDER BY s.rowid;

-- name: ListChats :many
SELECT
    c.id,
    c.channel_key,
    c.title,
    c.model_provider,
    c.model_id,
    c.thinking_level,
    c.cwd,
    c.created_at,
    c.archived_at,
    CAST(max(
        c.created_at,
        coalesce(max(s.created_at), c.created_at),
        coalesce(max(s.settled_at), c.created_at),
        coalesce(t.started_at, c.created_at),
        coalesce(t.ended_at, c.created_at)
    ) AS INTEGER) AS updated_at,
    CAST(coalesce(sum(s.status = 'pending'), 0) AS INTEGER) AS pending
FROM conversations c
LEFT JOIN submissions s ON s.conversation_id = c.id
LEFT JOIN tasks t ON t.conversation_id = c.id
WHERE c.agent_id = ?
    AND (c.channel_key LIKE 'chat:%' OR c.channel_key LIKE 'task:%' OR c.channel_key LIKE 'automation:%')
    AND (c.channel_key NOT LIKE 'chat:%' OR EXISTS (
        SELECT 1 FROM submissions m WHERE m.conversation_id = c.id AND m.kind = 'message'
    ))
GROUP BY c.id
ORDER BY updated_at DESC, c.id DESC;

-- name: GetChat :one
SELECT
    c.id,
    c.channel_key,
    c.title,
    c.model_provider,
    c.model_id,
    c.thinking_level,
    c.cwd,
    c.created_at,
    c.archived_at,
    c.execution_id,
    CAST(max(
        c.created_at,
        coalesce(max(s.created_at), c.created_at),
        coalesce(max(s.settled_at), c.created_at),
        coalesce(t.started_at, c.created_at),
        coalesce(t.ended_at, c.created_at)
    ) AS INTEGER) AS updated_at,
    CAST(coalesce(sum(s.status = 'pending'), 0) AS INTEGER) AS pending
FROM conversations c
LEFT JOIN submissions s ON s.conversation_id = c.id
LEFT JOIN tasks t ON t.conversation_id = c.id
WHERE c.id = ? AND c.agent_id = ? AND c.channel_key NOT LIKE 'dream:%'
GROUP BY c.id;

-- name: ListOpenSubmissions :many
-- The messages of a chat that have no answer: pending, or unanswered with a reason.
SELECT id, content, attachments, origin, status, reason, created_at FROM submissions
WHERE conversation_id = ? AND status != 'done' AND kind = 'message'
ORDER BY created_at;

-- name: ListProviders :many
SELECT * FROM providers ORDER BY created_at;

-- name: GetProvider :one
SELECT * FROM providers WHERE id = ?;

-- name: UpsertProvider :exec
INSERT INTO providers (id, name, api, base_url, api_key, models, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    name = excluded.name,
    api = excluded.api,
    base_url = excluded.base_url,
    api_key = excluded.api_key,
    models = excluded.models;

-- name: DeleteProvider :exec
DELETE FROM providers WHERE id = ?;

-- name: GetTelegramSettings :one
SELECT * FROM telegram_settings WHERE agent_id = ?;

-- name: SetTelegramSettings :exec
INSERT INTO telegram_settings (agent_id, token, allowed_users)
VALUES (?, ?, ?)
ON CONFLICT (agent_id) DO UPDATE SET token = excluded.token, allowed_users = excluded.allowed_users, pairing_code = '', pairing_expires = 0;

-- name: DeleteTelegramSettings :exec
DELETE FROM telegram_settings WHERE agent_id = ?;

-- name: CreateAccessTokenIfMissing :exec
INSERT INTO access_token (id, token) VALUES (1, ?) ON CONFLICT (id) DO NOTHING;

-- name: GetAccessToken :one
SELECT token FROM access_token WHERE id = 1;

-- name: CreateDream :exec
INSERT INTO dreams (id, agent_id, through_settled_at, through_id, created_at)
VALUES (?, ?, ?, ?, ?);

-- name: LatestDream :one
-- The newest dream of an agent. status is NULL when its message was never stored.
SELECT
    d.id,
    d.created_at,
    s.status
FROM dreams d
LEFT JOIN submissions s ON s.id = d.id
WHERE d.agent_id = ?
ORDER BY d.created_at DESC, d.id DESC
LIMIT 1;

-- name: DreamBoundary :one
-- Where the newest done dream of an agent stopped reading.
SELECT
    d.through_settled_at,
    d.through_id
FROM dreams d
JOIN submissions s ON s.id = d.id
WHERE d.agent_id = ? AND s.status = 'done'
ORDER BY d.created_at DESC, d.id DESC
LIMIT 1;

-- name: ListDreamInput :many
-- The settled messages of an agent's chats after a dream boundary, oldest first.
SELECT
    s.id,
    s.content,
    s.status,
    s.answer,
    s.reason,
    s.created_at,
    CAST(s.settled_at AS INTEGER) AS settled_at,
    c.id AS chat_id,
    c.title
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE
    c.agent_id = @agent_id
    AND c.channel_key NOT LIKE 'dream:%' AND c.channel_key NOT LIKE 'task:%'
    AND s.kind = 'message'
    AND s.status != 'pending'
    AND (s.settled_at > @after_settled_at OR (s.settled_at = @after_settled_at AND s.id > @after_id))
ORDER BY s.settled_at, s.id
LIMIT @max_rows;

-- name: ListAgentIDs :many
SELECT id FROM agents ORDER BY id;

-- name: CreateAutomation :exec
INSERT INTO automations (
    id, agent_id, name, prompt, schedule, time_zone, session, enabled, next_due_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateAutomation :execrows
UPDATE automations SET
    name = ?, prompt = ?, schedule = ?, time_zone = ?, session = ?, enabled = ?, next_due_at = ?,
    updated_at = ?
WHERE id = ? AND agent_id = ?;

-- name: GetAutomation :one
SELECT * FROM automations WHERE id = ? AND agent_id = ?;

-- name: ListAutomations :many
SELECT * FROM automations WHERE agent_id = ? ORDER BY created_at, id;

-- name: ListDueAutomations :many
SELECT * FROM automations WHERE enabled = 1 AND next_due_at <= ? ORDER BY next_due_at;

-- name: SetAutomationNextDue :exec
UPDATE automations SET next_due_at = ? WHERE id = ?;

-- name: DeleteAutomationRuns :exec
DELETE FROM automation_runs WHERE automation_id = ?;

-- name: DeleteAutomation :exec
DELETE FROM automations WHERE id = ? AND agent_id = ?;

-- name: CreateAutomationRun :execrows
-- A run for a scheduled time that has a run already is not stored again.
INSERT INTO automation_runs (id, automation_id, due_at, status, reason, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: FailAutomationRun :exec
UPDATE automation_runs SET status = 'failed', reason = ? WHERE id = ?;

-- name: ListAutomationRuns :many
-- The newest runs of an automation, with the message of each started run.
SELECT
    r.id,
    r.due_at,
    r.status,
    r.reason,
    r.created_at,
    s.status AS message_status,
    s.reason AS message_reason,
    s.conversation_id AS chat_id
FROM automation_runs r
LEFT JOIN submissions s ON s.id = r.id
WHERE r.automation_id = ?
ORDER BY r.created_at DESC, r.id DESC
LIMIT ?;

-- name: CountAutomationPending :one
-- The messages without an answer in the chats of an automation: automation:<id> and
-- automation:<id>:<run id>.
SELECT count(*) FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE
    s.status = 'pending'
    AND (c.channel_key = 'automation:' || CAST(@automation_id AS TEXT)
        OR c.channel_key LIKE 'automation:' || CAST(@automation_id AS TEXT) || ':%');

-- name: SetTelegramPairing :exec
UPDATE telegram_settings SET pairing_code = ?, pairing_expires = ? WHERE agent_id = ?;

-- name: PairTelegramOwner :one
UPDATE telegram_settings
SET allowed_users = CAST(sqlc.arg(user_id) AS TEXT),
    pairing_code = '', pairing_expires = 0
WHERE agent_id = sqlc.arg(agent_id) AND allowed_users = '' AND token = sqlc.arg(token) AND pairing_code = sqlc.arg(code)
    AND pairing_code <> '' AND pairing_expires > sqlc.arg(now)
RETURNING allowed_users;

-- name: CountPendingChat :one
SELECT count(*) FROM submissions WHERE conversation_id = ? AND status = 'pending';

-- name: AdmitSubmission :exec
UPDATE submissions SET admitted_at = ? WHERE id = ? AND admitted_at IS NULL;

-- name: RequestExecutionAbort :exec
UPDATE conversations SET abort_pending = 1 WHERE id = ? AND execution_id IS NOT NULL;

-- name: FinishExecutionAbort :exec
UPDATE conversations SET abort_pending = 0 WHERE agent_id = ? AND execution_id = ?;

-- name: ListPendingExecutionAborts :many
SELECT id, agent_id, execution_id FROM conversations WHERE abort_pending = 1;

-- name: SetConversationCwd :exec
UPDATE conversations SET cwd = ? WHERE id = ? AND agent_id = ?;

-- name: ListRecentProjects :many
WITH activity AS (
    SELECT c.cwd, c.id, max(
        c.created_at,
        coalesce(max(s.created_at), c.created_at),
        coalesce(max(s.settled_at), c.created_at),
        coalesce(t.started_at, c.created_at),
        coalesce(t.ended_at, c.created_at)
    ) AS updated_at
    FROM conversations c
    LEFT JOIN submissions s ON s.conversation_id = c.id
    LEFT JOIN tasks t ON t.conversation_id = c.id
    WHERE c.agent_id = ? AND c.cwd <> ''
    GROUP BY c.id
)
SELECT cwd FROM activity
GROUP BY cwd
ORDER BY max(updated_at) DESC, max(id) DESC;
