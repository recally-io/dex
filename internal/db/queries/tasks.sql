-- name: GetConversationByWorker :one
SELECT * FROM conversations WHERE agent_id = ? AND worker_conversation_id = ?;

-- name: CreateTask :exec
INSERT INTO tasks (id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'queued', ?);

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ?;

-- name: GetTaskByKey :one
SELECT * FROM tasks WHERE spawn_key = ?;

-- name: ListAgentTasks :many
SELECT t.id, t.parent_conversation_id, t.conversation_id, t.title, t.status,
    t.created_at, t.started_at, t.ended_at
FROM tasks t JOIN conversations c ON c.id = t.conversation_id
WHERE c.agent_id = ? ORDER BY t.created_at DESC, t.id DESC;

-- name: GetAgentTask :one
SELECT t.* FROM tasks t JOIN conversations c ON c.id = t.conversation_id
WHERE t.id = ? AND c.agent_id = ?;

-- name: ListTasks :many
SELECT t.* FROM tasks t
WHERE t.parent_conversation_id = @parent_id AND
(t.ended_at IS NULL OR t.id IN (SELECT recent.id FROM tasks recent WHERE recent.parent_conversation_id = @parent_id
AND recent.ended_at IS NOT NULL ORDER BY recent.ended_at DESC, recent.id DESC LIMIT 10))
ORDER BY t.created_at DESC, t.id DESC;

-- name: CountTaskSlots :one
SELECT count(*) FROM tasks t JOIN conversations c ON c.id = t.conversation_id
WHERE c.agent_id = ? AND t.status IN ('queued', 'running');

-- name: StartTask :execrows
UPDATE tasks SET status = 'running', started_at = @now
WHERE id = @id AND status = 'queued';

-- name: SettleTask :execrows
UPDATE tasks SET status = ?, report = ?, ended_at = ?
WHERE id = ? AND status IN ('queued', 'running');

-- name: ListActiveTasks :many
SELECT * FROM tasks WHERE parent_conversation_id = ? AND status IN ('queued', 'running');

-- name: CreateTaskResult :exec
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at)
SELECT @id, @conversation_id, @content, 'null', @source_key, source.origin, 'task_result', 'pending', @created_at
FROM tasks t JOIN submissions source ON source.id = t.source_request_id
WHERE t.id = @task_id
ON CONFLICT DO NOTHING;

-- name: ListLiveTasks :many
SELECT * FROM tasks WHERE status = 'running';

-- name: ListTaskRecoveries :many
SELECT t.id, c.agent_id, c.worker_conversation_id FROM tasks t
JOIN conversations c ON c.id = t.conversation_id
WHERE c.worker_conversation_id IS NOT NULL AND t.started_at IS NOT NULL;

-- name: ListStopConversations :many
SELECT * FROM conversations WHERE agent_id = @agent_id
AND (id = @chat_id OR (@peer != '' AND (channel_key = @peer OR substr(channel_key, 1, length(@peer) + 1) = @peer || '/')))
ORDER BY id;

-- name: StopConversationSubmissions :execrows
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE conversation_id = @chat_id AND status = 'pending';

-- name: GetTaskResultLabel :one
SELECT t.id, t.title FROM submissions s
JOIN tasks t ON s.id = 'task:' || t.id || ':result'
WHERE s.id = @result_id AND s.conversation_id = @chat_id AND s.kind = 'task_result'
AND t.parent_conversation_id = s.conversation_id;
