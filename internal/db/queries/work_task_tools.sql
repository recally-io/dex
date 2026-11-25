-- name: ListAgentWorkTasks :many
SELECT t.*, CAST(coalesce(s.conversation_id, 0) AS INTEGER) AS session_id
FROM work_tasks t JOIN agents a ON a.id = t.agent_id LEFT JOIN work_task_sessions s ON s.task_id = t.id AND s.is_primary = 1
WHERE t.agent_id = @agent_id AND a.archived_at IS NULL AND (t.lifecycle = 'open' OR sqlc.arg(include_closed))
ORDER BY t.updated_at DESC, t.id DESC LIMIT 50;

-- name: RenameWorkTask :one
UPDATE work_tasks SET title = @title, updated_at = @updated_at
WHERE work_tasks.id = @id AND work_tasks.agent_id = @agent_id AND EXISTS (SELECT 1 FROM agents a WHERE a.id = @agent_id AND a.archived_at IS NULL)
RETURNING *;

-- name: ListWorkTaskSessions :many
SELECT s.conversation_id, s.agent_id, a.name AS agent_name, c.title, s.is_primary, s.linked_at
FROM work_task_sessions s JOIN conversations c ON c.id = s.conversation_id JOIN agents a ON a.id = s.agent_id
WHERE s.task_id = ? ORDER BY s.is_primary DESC, s.linked_at, s.conversation_id;

-- name: LinkWorkTaskSession :exec
-- Keep the original timestamp on replay. The first link becomes primary.
INSERT INTO work_task_sessions (task_id, conversation_id, agent_id, is_primary, linked_at)
SELECT t.id, c.id, c.agent_id,
  NOT EXISTS (SELECT 1 FROM work_task_sessions s WHERE s.task_id = t.id AND s.is_primary = 1), @linked_at
FROM work_tasks t JOIN conversations c ON c.id = @conversation_id
WHERE t.id = @task_id AND (CAST(sqlc.arg(only_open) AS INTEGER) = 0 OR t.lifecycle = 'open')
ON CONFLICT (task_id, conversation_id) DO NOTHING;

-- name: ClearWorkTaskPrimarySession :exec
UPDATE work_task_sessions SET is_primary = 0 WHERE task_id = ? AND is_primary = 1;

-- name: PromoteWorkTaskSession :exec
UPDATE work_task_sessions SET is_primary = 1 WHERE task_id = ? AND conversation_id = ?;

-- name: ListWorkTaskTimeline :many
SELECT en.id, en.content, en.created_at, en.author, CAST(coalesce(en.event_id, '') AS TEXT) AS event_id,
  CAST(coalesce(ev.event_type, '') AS TEXT) AS event_type, CAST(coalesce(ev.source_url, '') AS TEXT) AS source_url,
  CAST(coalesce(ev.author_association, '') AS TEXT) AS author_association
FROM work_task_entries en LEFT JOIN work_source_events ev ON ev.id = en.event_id
WHERE en.task_id = @task_id ORDER BY en.id DESC LIMIT @max_rows;

-- name: GetWorkTaskToolOperation :one
SELECT * FROM work_task_tool_operations WHERE key = ?;

-- name: CreateWorkTaskToolOperation :exec
INSERT INTO work_task_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
VALUES (?, ?, ?, ?, ?, ?);
