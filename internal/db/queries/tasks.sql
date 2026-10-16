-- name: GetConversationByWorker :one
SELECT * FROM conversations WHERE agent_id = ? AND worker_conversation_id = ?;

-- name: CreateTask :exec
INSERT INTO tasks (id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, status, last_event_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?);

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ?;

-- name: GetTaskByKey :one
SELECT * FROM tasks WHERE spawn_key = ?;

-- name: ListTasks :many
SELECT t.* FROM tasks t
WHERE t.parent_conversation_id = @parent_id AND
(t.ended_at IS NULL OR t.id IN (SELECT recent.id FROM tasks recent WHERE recent.parent_conversation_id = @parent_id
AND recent.ended_at IS NOT NULL ORDER BY recent.ended_at DESC, recent.id DESC LIMIT 10))
ORDER BY t.created_at DESC, t.id DESC;

-- name: CountTaskSlots :one
SELECT count(*) FROM tasks t JOIN conversations c ON c.id = t.conversation_id
WHERE c.agent_id = ? AND t.status IN ('queued', 'running', 'cancelling');

-- name: StartTask :execrows
UPDATE tasks SET status = 'running', started_at = coalesce(started_at, @now), last_event_at = @now
WHERE id = @id AND status = 'queued';

-- name: UpdateTaskProgress :exec
UPDATE tasks SET current_tool = ?, last_event_at = ?
WHERE id = ? AND status IN ('running', 'cancelling');

-- name: SettleTask :exec
UPDATE tasks SET status = ?, report = ?, stop_note = ?, current_tool = '', last_event_at = ?, ended_at = ?
WHERE id = ?;

-- name: RequestTaskStop :exec
UPDATE tasks SET stop_requested_at = coalesce(stop_requested_at, @now), status = 'cancelling'
WHERE id = @id AND status IN ('queued', 'running', 'cancelling');

-- name: StopConversationTasks :exec
UPDATE tasks SET stop_requested_at = coalesce(stop_requested_at, @now), status = 'cancelling'
WHERE parent_conversation_id = @parent_id AND status IN ('queued', 'running', 'cancelling');

-- name: ListCancellingTasks :many
SELECT t.*, c.agent_id, c.worker_conversation_id FROM tasks t
JOIN conversations c ON c.id = t.conversation_id WHERE t.status = 'cancelling';

-- name: CreateTaskResult :exec
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, kind, status, created_at)
VALUES (?, ?, ?, 'null', ?, 'task_result', 'pending', ?)
ON CONFLICT DO NOTHING;

-- name: ListLiveTasks :many
SELECT t.*, c.agent_id, c.worker_conversation_id FROM tasks t
JOIN conversations c ON c.id = t.conversation_id
WHERE t.status IN ('running', 'cancelling');

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
