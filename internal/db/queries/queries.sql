-- name: CreateAgentIfMissing :exec
INSERT INTO agents (id, name, model_provider, model_id, created_at)
VALUES (?, ?, '', '', ?)
ON CONFLICT (id) DO NOTHING;

-- name: UpdateAgentSettings :exec
UPDATE agents SET model_provider = ?, model_id = ?, instructions = ? WHERE id = ?;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: EnsureConversation :one
INSERT INTO conversations (agent_id, channel_key, created_at)
VALUES (?, ?, ?)
ON CONFLICT (agent_id, channel_key) DO UPDATE SET agent_id = excluded.agent_id
RETURNING *;

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = ?;

-- name: SetWorkerConversation :exec
UPDATE conversations SET worker_conversation_id = ? WHERE id = ?;

-- name: CreateSubmission :execrows
INSERT INTO submissions (id, conversation_id, content, source_key, status, created_at)
VALUES (?, ?, ?, ?, 'pending', ?)
ON CONFLICT (source_key) DO NOTHING;

-- name: GetSubmission :one
SELECT * FROM submissions WHERE id = ?;

-- name: SettleSubmission :execrows
UPDATE submissions SET status = ?, answer = ?, reason = ?, settled_at = ?
WHERE id = ? AND status = 'pending';

-- name: GetSubmissionReply :one
SELECT
    s.status,
    s.answer,
    s.reason,
    c.channel_key
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.id = ?;

-- name: ListPendingSubmissions :many
SELECT
    s.id,
    s.content,
    s.conversation_id,
    c.agent_id
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending'
ORDER BY s.created_at;

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
SELECT * FROM telegram_settings WHERE id = 1;

-- name: SetTelegramSettings :exec
INSERT INTO telegram_settings (id, token, allowed_users)
VALUES (1, ?, ?)
ON CONFLICT (id) DO UPDATE SET token = excluded.token, allowed_users = excluded.allowed_users;

-- name: DeleteTelegramSettings :exec
DELETE FROM telegram_settings WHERE id = 1;
