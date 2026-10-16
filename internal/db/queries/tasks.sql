-- name: GetConversationByWorker :one
SELECT * FROM conversations WHERE agent_id = ? AND worker_conversation_id = ?;

-- name: CreateUserTask :exec
INSERT INTO user_tasks (id, short_id, parent_conversation_id, title, status, created_at)
VALUES (?, ?, ?, ?, 'active', ?);

-- name: GetUserTask :one
SELECT t.* FROM user_tasks t JOIN conversations c ON c.id = t.parent_conversation_id
WHERE c.agent_id = @agent_id AND t.parent_conversation_id = @parent_id
AND (t.id = @task_id OR t.short_id = @task_id);

-- name: ListUserTasks :many
SELECT t.* FROM user_tasks t WHERE t.parent_conversation_id = @parent_id AND
(t.ended_at IS NULL OR t.id IN (SELECT recent.id FROM user_tasks recent WHERE recent.parent_conversation_id = @parent_id
AND recent.ended_at IS NOT NULL ORDER BY recent.ended_at DESC, recent.id DESC LIMIT 10))
ORDER BY t.created_at DESC, t.id DESC;

-- name: CreateTaskStage :exec
INSERT INTO task_stages (id, user_task_id, conversation_id, spawn_key, source_request_id, title, status, last_event_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?);

-- name: GetTaskStage :one
SELECT * FROM task_stages WHERE id = ?;

-- name: GetTaskStageByKey :one
SELECT * FROM task_stages WHERE spawn_key = ?;

-- name: ListTaskStages :many
SELECT s.*, c.agent_id, c.model_provider, c.model_id, c.thinking_level,
c.worker_conversation_id FROM task_stages s JOIN conversations c ON c.id = s.conversation_id
WHERE s.user_task_id = ? ORDER BY s.created_at, s.id;

-- name: CountTaskSlots :one
SELECT count(*) FROM task_stages s JOIN conversations c ON c.id = s.conversation_id
WHERE c.agent_id = ? AND s.status IN ('queued', 'running', 'cancelling', 'unknown');

-- name: StartTaskStage :execrows
UPDATE task_stages SET status = 'running', started_at = coalesce(started_at, @now), last_event_at = @now
WHERE id = @id AND status = 'queued';

-- name: UpdateTaskProgress :exec
UPDATE task_stages SET current_tool = ?, last_event_at = ?
WHERE id = ? AND status IN ('running', 'cancelling');

-- name: SettleTaskStage :execrows
UPDATE task_stages SET status = ?, report = ?, current_tool = '', last_event_at = ?, ended_at = ?
WHERE id = ? AND status IN ('queued', 'running', 'cancelling', 'unknown');

-- name: SetUserTaskState :exec
UPDATE user_tasks SET status = ?, report = ?, ended_at = ? WHERE id = ?;

-- name: GetTaskFinish :one
SELECT user_task_id FROM task_finishes WHERE call_key = ?;

-- name: CreateTaskFinish :exec
INSERT INTO task_finishes (call_key, user_task_id) VALUES (?, ?);

-- name: RequestTaskStop :exec
UPDATE user_tasks SET stop_requested_at = coalesce(stop_requested_at, ?), status = 'cancelling'
WHERE id = ? AND ended_at IS NULL;

-- name: CancelTaskStages :exec
UPDATE task_stages SET status = 'cancelling'
WHERE user_task_id = ? AND status IN ('queued', 'running');

-- name: ListCancellingStages :many
SELECT s.*, c.agent_id, c.worker_conversation_id FROM task_stages s
JOIN conversations c ON c.id = s.conversation_id WHERE s.status = 'cancelling';

-- name: CreateTaskResult :exec
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, kind, status, created_at)
VALUES (?, ?, ?, 'null', ?, 'task_result', 'pending', ?)
ON CONFLICT DO NOTHING;

-- name: CreateTaskNotice :exec
INSERT INTO task_notices (id, user_task_id, channel_key, text, created_at)
VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING;

-- name: ListTaskNotices :many
SELECT * FROM task_notices WHERE delivered_at IS NULL AND channel_key LIKE @prefix || '%'
ORDER BY created_at, id;

-- name: AdvanceTaskNotice :exec
UPDATE task_notices SET delivered_parts = delivered_parts + 1 WHERE id = ?;

-- name: MarkTaskNoticeDelivered :exec
UPDATE task_notices SET delivered_at = ? WHERE id = ?;

-- name: AdvanceChannelCommand :exec
UPDATE channel_commands SET delivered_parts = delivered_parts + 1 WHERE source_key = ?;

-- name: ListLiveTaskStages :many
SELECT s.*, c.agent_id, c.worker_conversation_id FROM task_stages s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.status IN ('running', 'cancelling', 'unknown');

-- name: MarkTaskStageUnknown :exec
UPDATE task_stages SET status = 'unknown', report = ?, last_event_at = ? WHERE id = ?
AND status IN ('running', 'cancelling');

-- name: GetTaskStageByConversation :one
SELECT * FROM task_stages WHERE conversation_id = ?;

-- name: GetTaskStageByResult :one
SELECT s.* FROM task_stages s JOIN submissions r ON r.source_key = 'task:' || s.id || ':result'
WHERE r.id = ? AND r.kind = 'task_result';

-- name: GetUserTaskByID :one
SELECT * FROM user_tasks WHERE id = ?;

-- name: AdvanceTaskResultAttempt :exec
UPDATE submissions SET delivery_attempt = delivery_attempt + 1 WHERE id = ? AND kind = 'task_result';

-- name: ListTaskRecoveries :many
SELECT s.id, c.agent_id, c.worker_conversation_id FROM task_stages s
JOIN conversations c ON c.id = s.conversation_id
WHERE c.worker_conversation_id IS NOT NULL
AND (s.status NOT IN ('queued', 'cancelling') OR s.started_at IS NOT NULL);

-- name: ListStoppingTasks :many
SELECT * FROM user_tasks t WHERE t.status = 'cancelling' AND t.ended_at IS NULL
AND NOT EXISTS (SELECT 1 FROM task_stages s WHERE s.user_task_id = t.id
AND s.status IN ('queued', 'running', 'cancelling'));
