-- name: UpsertAgent :exec
INSERT INTO agents (id, name, model_provider, model_id, instructions, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    name = excluded.name,
    model_provider = excluded.model_provider,
    model_id = excluded.model_id,
    instructions = excluded.instructions;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: EnsureConversation :one
INSERT INTO conversations (agent_id, channel_key, created_at)
VALUES (?, ?, ?)
ON CONFLICT (agent_id, channel_key) DO UPDATE SET agent_id = excluded.agent_id
RETURNING *;

-- name: SetWorkerConversation :exec
UPDATE conversations SET worker_conversation_id = ? WHERE id = ?;

-- name: CreateSubmission :exec
INSERT INTO submissions (id, conversation_id, content, status, created_at)
VALUES (?, ?, ?, 'pending', ?);

-- name: GetSubmission :one
SELECT * FROM submissions WHERE id = ?;

-- name: SettleSubmission :exec
UPDATE submissions SET status = ?, answer = ?, reason = ?, settled_at = ?
WHERE id = ? AND status = 'pending';

-- name: ListPendingSubmissions :many
SELECT
    s.id,
    s.content,
    c.agent_id,
    c.worker_conversation_id
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending' AND c.worker_conversation_id IS NOT NULL
ORDER BY s.created_at;
