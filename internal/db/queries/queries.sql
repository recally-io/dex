-- name: CreateAgentIfMissing :exec
INSERT INTO agents (id, name, model_provider, model_id, created_at)
VALUES (?, ?, '', '', ?)
ON CONFLICT (id) DO NOTHING;

-- name: UpdateAgentSettings :exec
UPDATE agents SET
    name = @name,
    model_provider = @model_provider,
    model_id = @model_id,
    thinking_level = @thinking_level,
    fallback_provider = @fallback_provider,
    fallback_model_id = @fallback_model_id,
    instructions = @instructions,
    runner_sandbox_id = CASE WHEN runner_id = @runner_id THEN runner_sandbox_id ELSE '' END,
    runner_id = @runner_id
WHERE id = @id;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: ListAgents :many
-- Active bots only. Startup starts channels for these bots.
SELECT * FROM agents WHERE archived_at IS NULL ORDER BY created_at, id;

-- name: ListArchivedAgents :many
SELECT * FROM agents WHERE archived_at IS NOT NULL ORDER BY archived_at DESC, id;

-- name: AgentArchiveBlocker :one
-- Why a bot cannot be archived now, or an empty string. Busy means any pending input in any
-- conversation of the bot, including automation runs and dreams. A queued task has a pending
-- input too, so the check needs no task table.
SELECT CAST(CASE
    WHEN NOT EXISTS (SELECT 1 FROM agents WHERE agents.id = @agent_id) THEN 'missing'
    WHEN @agent_id = 'default' THEN 'default'
    WHEN EXISTS (SELECT 1 FROM agents WHERE agents.id = @agent_id AND archived_at IS NOT NULL) THEN 'archived'
    WHEN EXISTS (
        SELECT 1 FROM submissions s JOIN conversations c ON c.id = s.conversation_id
        WHERE c.agent_id = @agent_id AND s.status = 'pending'
    ) THEN 'busy'
    ELSE ''
END AS TEXT);

-- name: AgentRunningSessions :many
-- The conversations of each bot that have a pending input, Main included. This is the busy rule of
-- AgentArchiveBlocker, so a bot with a count above zero is a bot that cannot be archived.
SELECT c.agent_id, CAST(count(DISTINCT c.id) AS INTEGER) AS sessions
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending'
GROUP BY c.agent_id;

-- name: AgentArchived :one
-- Input writers call this after they hold the SQLite writer, so an input and an archive are ordered.
SELECT CAST(EXISTS (SELECT 1 FROM agents WHERE agents.id = @agent_id AND archived_at IS NOT NULL) AS INTEGER);

-- name: ConversationAgentArchived :one
SELECT CAST(EXISTS (SELECT 1 FROM conversations c JOIN agents a ON a.id = c.agent_id
    WHERE c.id = @conversation_id AND a.archived_at IS NOT NULL) AS INTEGER);

-- name: ArchiveAgent :execrows
-- One statement holds the rules of AgentArchiveBlocker, so a caller outside a transaction cannot
-- archive a bot that has just become busy.
UPDATE agents SET archived_at = @now
WHERE agents.id = @agent_id AND archived_at IS NULL AND agents.id <> 'default'
    AND NOT EXISTS (
        SELECT 1 FROM submissions s JOIN conversations c ON c.id = s.conversation_id
        WHERE c.agent_id = agents.id AND s.status = 'pending'
    );

-- name: RestoreAgent :execrows
UPDATE agents SET archived_at = NULL WHERE id = ? AND archived_at IS NOT NULL;

-- name: CreateAgent :one
INSERT INTO agents (id, name, model_provider, model_id, thinking_level, fallback_provider, fallback_model_id, instructions, runner_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: EnsureConversation :one
-- The title and the settings are set only when the conversation is created.
INSERT INTO conversations (agent_id, channel_key, title, model_provider, model_id, thinking_level, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id, channel_key) DO UPDATE SET agent_id = excluded.agent_id
    WHERE conversations.channel_key NOT LIKE 'retired-telegram:%'
RETURNING *;

-- name: EnsureEmptyChat :one
-- One write statement serializes concurrent opens and first-message admission.
INSERT INTO conversations (agent_id, channel_key, title, created_at)
VALUES (
    @agent_id,
    coalesce((
        SELECT c.channel_key FROM conversations c
        WHERE c.agent_id = @agent_id AND c.channel_key LIKE 'chat:%' AND c.archived_at IS NULL
            AND c.forked_from IS NULL
            AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.conversation_id = c.id AND s.kind = 'message')
        ORDER BY c.created_at DESC, c.id DESC LIMIT 1
    ), @channel_key),
    '', @created_at
)
ON CONFLICT (agent_id, channel_key) DO UPDATE SET agent_id = excluded.agent_id
RETURNING *;

-- name: CreateFork :one
-- A fork is a new Chat with the settings and the parent of its source. execution_id is NULL when
-- the fork starts empty, so the first message creates the native conversation.
INSERT INTO conversations (agent_id, channel_key, title, model_provider, model_id, thinking_level, cwd, runner_id, execution_id, forked_from, parent_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ArchiveChat :exec
-- Keep the first archive time. The owner can archive Chats and child sessions.
UPDATE conversations SET archived_at = coalesce(archived_at, @archived_at)
WHERE id = @id AND agent_id = @agent_id AND channel_key NOT LIKE 'retired-telegram:%' AND (channel_key LIKE 'chat:%' OR parent_id IS NOT NULL);

-- name: UnarchiveChat :exec
UPDATE conversations SET archived_at = NULL
WHERE id = @id AND agent_id = @agent_id AND channel_key NOT LIKE 'retired-telegram:%' AND (channel_key LIKE 'chat:%' OR parent_id IS NOT NULL);

-- name: SetFirstChatTitle :exec
UPDATE conversations SET title = ? WHERE id = ? AND title = '';

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = ? AND channel_key NOT LIKE 'retired-telegram:%';

-- name: GetConversationForStop :one
-- Startup also stops retired sessions that still have a native execution.
SELECT * FROM conversations WHERE id = ?;

-- name: GetConversationByKey :one
SELECT * FROM conversations WHERE agent_id = ? AND channel_key = ? AND channel_key NOT LIKE 'retired-telegram:%';

-- name: SetConversationSettings :exec
UPDATE conversations SET model_provider = ?, model_id = ?, thinking_level = ?
WHERE id = ? AND agent_id = ?;

-- name: SetExecution :exec
UPDATE conversations SET execution_id = ? WHERE id = ?;

-- name: CreateSubmission :execrows
-- A message whose ID or source key exists already is not stored again. An empty when_busy
-- keeps the column default, so only callers that choose a mode name one.
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, producer, reply_channel, reply_peer, kind, when_busy, status, created_at, channel_context)
VALUES (
    @id,
    @conversation_id,
    @content,
    @attachments,
    @source_key,
    @origin,
    @producer,
    coalesce(CAST(@reply_channel AS TEXT), ''),
    coalesce(CAST(@reply_peer AS TEXT), ''),
    @kind,
    coalesce(nullif(CAST(@when_busy AS TEXT), ''), 'followUp'),
    'pending',
    @created_at,
    coalesce(nullif(CAST(@channel_context AS TEXT), ''), '{}')
)
ON CONFLICT DO NOTHING;

-- name: GetSubmission :one
SELECT * FROM submissions WHERE id = ?;

-- name: SettleSubmission :execrows
UPDATE submissions SET status = ?, answer = ?, reason = ?, settled_at = ?
WHERE id = ? AND status = 'pending';

-- name: WithdrawSubmission :execrows
-- A user withdraws one queued input. The settle worker can record the native abort first, so
-- that reason also qualifies. The receipt and its source key stay, so a redelivery does not run
-- the input. A withdrawn input has no reply to deliver to a channel.
UPDATE submissions SET status = 'unanswered', answer = NULL, reason = 'withdrawn',
    settled_at = coalesce(settled_at, @now), delivered_at = coalesce(delivered_at, @now)
WHERE id = @id AND kind IN ('message', 'session_result', 'wake')
AND (status = 'pending' OR (status = 'unanswered' AND reason = 'aborted'));

-- name: GetSubmissionReply :one
SELECT
    s.origin,
    c.id AS conversation_id
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.id = ?;

-- name: ListPendingSubmissions :many
-- SQLite assigns rowids under the writer lock. Keep receipt order, with Task wakes last.
SELECT
    s.id,
    s.content,
    s.attachments,
    s.kind,
    s.origin,
    s.producer,
    s.reply_channel,
    s.reply_peer,
    s.created_at,
    s.admitted_at,
    s.when_busy,
    s.conversation_id,
    c.agent_id,
    c.channel_key
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending' AND c.channel_key NOT LIKE 'retired-telegram:%' AND (CAST(@conversation_id AS INTEGER) = 0 OR s.conversation_id = @conversation_id)
ORDER BY (s.kind = 'task_wake'), s.rowid;

-- name: ListChats :many
-- The sessions of one bot, and the children of its sessions that belong to another bot.
SELECT
    c.id,
    c.agent_id,
    a.name AS agent_name,
    p.agent_id AS parent_agent_id,
    pa.name AS parent_agent_name,
    c.channel_key,
    c.title,
    c.model_provider,
    c.model_id,
    c.thinking_level,
    c.cwd,
    c.runner_id,
    c.created_at,
    c.archived_at,
    c.parent_id,
    CAST(max(
        c.created_at,
        coalesce(max(s.created_at), c.created_at),
        coalesce(max(s.settled_at), c.created_at)
    ) AS INTEGER) AS updated_at,
    CAST(coalesce(sum(s.status = 'pending'), 0) AS INTEGER) AS pending
FROM conversations c
JOIN agents a ON a.id = c.agent_id
LEFT JOIN conversations p ON p.id = c.parent_id
LEFT JOIN agents pa ON pa.id = p.agent_id
LEFT JOIN submissions s ON s.conversation_id = c.id
WHERE (c.agent_id = @agent_id OR p.agent_id = @agent_id) AND c.channel_key NOT LIKE 'retired-telegram:%'
    AND (c.channel_key LIKE 'chat:%' OR c.parent_id IS NOT NULL OR c.channel_key LIKE 'automation:%')
    AND (c.channel_key NOT LIKE 'chat:%' OR c.parent_id IS NOT NULL OR c.forked_from IS NOT NULL OR EXISTS (
        SELECT 1 FROM submissions m WHERE m.conversation_id = c.id AND m.kind = 'message'
    ))
GROUP BY c.id
ORDER BY updated_at DESC, c.id DESC;

-- name: GetChat :one
SELECT
    c.id,
    p.agent_id AS parent_agent_id,
    pa.name AS parent_agent_name,
    c.channel_key,
    c.title,
    c.model_provider,
    c.model_id,
    c.thinking_level,
    c.cwd,
    c.runner_id,
    c.created_at,
    c.archived_at,
    c.parent_id,
    c.execution_id,
    c.forked_from,
    f.title AS forked_from_title,
    f.channel_key AS forked_from_key,
    CAST(max(
        c.created_at,
        coalesce(max(s.created_at), c.created_at),
        coalesce(max(s.settled_at), c.created_at)
    ) AS INTEGER) AS updated_at,
    CAST(coalesce(sum(s.status = 'pending'), 0) AS INTEGER) AS pending
FROM conversations c
LEFT JOIN submissions s ON s.conversation_id = c.id
LEFT JOIN conversations f ON f.id = c.forked_from
LEFT JOIN conversations p ON p.id = c.parent_id
LEFT JOIN agents pa ON pa.id = p.agent_id
WHERE c.id = ? AND c.agent_id = ? AND c.channel_key NOT LIKE 'retired-telegram:%' AND c.channel_key NOT LIKE 'dream:%'
GROUP BY c.id;

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

-- name: SetTelegramSettings :one
INSERT INTO telegram_settings (agent_id, token, allowed_users, options, webhook_url, webhook_secret)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id) DO UPDATE SET token = excluded.token, allowed_users = excluded.allowed_users, options = excluded.options, pairing_code = '', pairing_expires = 0,
webhook_url = excluded.webhook_url, webhook_secret = excluded.webhook_secret,
retry_at = CASE WHEN substr(token, 1, instr(token, ':') - 1) = substr(excluded.token, 1, instr(excluded.token, ':') - 1) THEN retry_at ELSE 0 END
RETURNING retry_at;

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
    AND c.channel_key NOT LIKE 'dream:%' AND c.parent_id IS NULL
    AND c.channel_key NOT LIKE 'retired-telegram:%'
    AND s.kind = 'message'
    AND s.status != 'pending'
    AND (s.settled_at > @after_settled_at OR (s.settled_at = @after_settled_at AND s.id > @after_id))
ORDER BY s.settled_at, s.id
LIMIT @max_rows;

-- name: ListAgentIDs :many
SELECT id FROM agents WHERE archived_at IS NULL ORDER BY id;

-- name: CreateAutomation :exec
INSERT INTO automations (
    id, agent_id, name, prompt, schedule, time_zone, enabled, next_due_at, created_at, updated_at, cwd, notification_route, notification_target_id, runner_id, session, target_conversation_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateAutomation :execrows
UPDATE automations SET
    cwd = ?, name = ?, prompt = ?, schedule = ?, time_zone = ?, session = ?, enabled = ?, next_due_at = ?,
    notification_route = ?, notification_target_id = ?, runner_id = ?, updated_at = ?, target_conversation_id = ?
WHERE id = ? AND agent_id = ?;

-- name: GetAutomation :one
SELECT * FROM automations WHERE id = ? AND agent_id = ?;

-- name: ListAutomations :many
SELECT * FROM automations WHERE agent_id = ? ORDER BY created_at, id;

-- name: ListDueAutomations :many
SELECT a.* FROM automations a JOIN agents b ON b.id = a.agent_id
WHERE a.enabled = 1 AND a.next_due_at <= ? AND b.archived_at IS NULL ORDER BY a.next_due_at;

-- name: SetAutomationNextDue :execrows
UPDATE automations SET next_due_at = @next_due_at
WHERE id = @id AND next_due_at = @expected_due_at AND updated_at = @expected_updated_at
  AND schedule = @expected_schedule AND time_zone = @expected_time_zone AND enabled = 1;

-- name: DeleteAutomationRuns :exec
DELETE FROM automation_runs WHERE automation_id = ?;

-- name: DeleteAutomation :exec
DELETE FROM automations WHERE id = ? AND agent_id = ?;

-- name: CreateAutomationRun :execrows
-- A run for a scheduled time that has a run already is not stored again.
INSERT INTO automation_runs (id, automation_id, due_at, status, reason, created_at, runner_id, cwd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: ListAutomationRuns :many
-- The newest runs of an automation, with the message of each started run.
SELECT
    r.id,
    r.due_at,
    r.status,
    r.reason,
    r.created_at, r.outcome, r.summary, r.change_key, r.duplicate, r.runner_id, r.cwd, r.notification_note,
    s.status AS message_status,
    s.reason AS message_reason,
    s.conversation_id AS chat_id,
    s.admitted_at, s.settled_at
FROM automation_runs r
LEFT JOIN submissions s ON s.id = r.id
WHERE r.automation_id = sqlc.arg(automation_id)
ORDER BY r.created_at DESC, r.id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountAutomationPending :one
-- The messages without an answer in the chats of an automation: automation:<id> and
-- automation:<id>:<run id>.
SELECT count(*) FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE
    s.status = 'pending'
    AND (c.channel_key = 'automation:' || CAST(@automation_id AS TEXT)
        OR c.channel_key LIKE 'automation:' || CAST(@automation_id AS TEXT) || ':%');

-- name: ListAutomationTargetCandidates :many
SELECT * FROM conversations WHERE agent_id = ? AND channel_key NOT LIKE 'retired-telegram:%' ORDER BY created_at DESC, id DESC;

-- name: CountBoundAutomationPending :one
SELECT count(*) FROM automation_runs r
JOIN submissions s ON s.id = r.id
WHERE r.automation_id = ? AND s.status = 'pending';

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
UPDATE conversations SET abort_pending = 1 WHERE id = ?;

-- name: FinishExecutionAbort :exec
UPDATE conversations SET abort_pending = 0 WHERE id = ?;

-- name: ListPendingExecutionAborts :many
SELECT c.id, c.agent_id, c.execution_id FROM conversations c
WHERE c.abort_pending = 1;

-- name: SetConversationCwd :exec
UPDATE conversations SET cwd = ? WHERE id = ? AND agent_id = ?;

-- name: ListRecentProjects :many
WITH activity AS (
    SELECT c.cwd, c.id, max(
        c.created_at,
        coalesce(max(s.created_at), c.created_at),
        coalesce(max(s.settled_at), c.created_at)
    ) AS updated_at
    FROM conversations c
    LEFT JOIN submissions s ON s.conversation_id = c.id
    WHERE c.agent_id = ? AND c.cwd <> ''
    GROUP BY c.id
)
SELECT cwd FROM activity
GROUP BY cwd
ORDER BY max(updated_at) DESC, max(id) DESC;

-- name: GetAutomationRun :one
SELECT * FROM automation_runs WHERE id = ?;

-- name: GetPreviousFinishedAutomationRun :one
-- Choose the preceding run in run-list order, not the preceding failure. Skip active runs.
SELECT sqlc.embed(prior), s.status AS execution_status, s.reason AS execution_reason
FROM automation_runs current
JOIN automation_runs prior ON prior.automation_id = current.automation_id
LEFT JOIN submissions s ON s.id = prior.id
WHERE current.id = sqlc.arg(run_id)
  AND (prior.created_at, prior.id) < (current.created_at, current.id)
  AND (prior.status IN ('skipped', 'failed') OR s.status IN ('done', 'unanswered'))
ORDER BY prior.created_at DESC, prior.id DESC
LIMIT 1;

-- name: SetAutomationRunNotificationNote :exec
UPDATE automation_runs SET notification_note = ? WHERE id = ?;

-- name: ReportAutomationRun :execrows
UPDATE automation_runs SET outcome = @outcome, summary = @summary, change_key = @change_key
WHERE automation_runs.id = @id AND automation_runs.outcome = '' AND automation_runs.status = 'started'
    AND EXISTS (SELECT 1 FROM submissions WHERE id = @id AND status = 'pending');

-- name: FinalizeAutomationRun :exec
UPDATE automation_runs SET
    outcome = CASE WHEN NOT (CAST(@execution_status AS TEXT) = 'done') OR outcome = '' THEN 'failed' ELSE outcome END,
    summary = CASE
        WHEN NOT (CAST(@execution_status AS TEXT) = 'done') THEN @failure
        WHEN outcome = '' THEN 'run ended without automation_report'
        ELSE summary END,
    status = CASE WHEN NOT (CAST(@execution_status AS TEXT) = 'done') OR outcome IN ('', 'failed') THEN 'failed' ELSE status END,
    reason = CASE
        WHEN NOT (CAST(@execution_status AS TEXT) = 'done') THEN @failure
        WHEN outcome = '' THEN 'run ended without automation_report'
        WHEN outcome = 'failed' THEN summary
        ELSE reason END,
    duplicate = CASE WHEN (CAST(@execution_status AS TEXT) = 'done') AND outcome <> '' AND change_key <> '' AND EXISTS (
        SELECT 1 FROM automation_runs prior JOIN submissions s ON s.id = prior.id
        WHERE prior.automation_id = automation_runs.automation_id
            AND prior.id <> automation_runs.id AND prior.change_key = automation_runs.change_key
            AND prior.outcome = automation_runs.outcome AND s.status = 'done'
    ) THEN 1 ELSE 0 END
WHERE automation_runs.id = @id AND automation_runs.status = 'started';

-- name: GetOnePassword :one
SELECT mode, connect_host, connect_token, service_account_token FROM one_password WHERE id = 1;

-- name: SetOnePassword :exec
INSERT INTO one_password (id, mode, connect_host, connect_token, service_account_token)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET mode = excluded.mode, connect_host = excluded.connect_host,
    connect_token = excluded.connect_token, service_account_token = excluded.service_account_token;
