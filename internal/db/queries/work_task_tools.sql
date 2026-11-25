-- name: ListAgentWorkTasks :many
SELECT t.*, CAST(coalesce(s.conversation_id, 0) AS INTEGER) AS session_id
FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
LEFT JOIN task_executions s ON s.task_id = t.id AND s.kind = 'dex_session' AND s.is_primary = 1
WHERE t.assignee_bot_id = @agent_id AND a.archived_at IS NULL AND (t.status NOT IN ('done', 'cancelled') OR sqlc.arg(include_closed))
ORDER BY t.updated_at DESC, t.id DESC LIMIT 50;

-- name: ListWorkTaskSessions :many
SELECT c.id AS conversation_id, s.bot_id AS agent_id, a.name AS agent_name, c.title, s.is_primary, s.started_at AS linked_at
FROM task_executions s JOIN conversations c ON c.id = s.conversation_id JOIN agents a ON a.id = s.bot_id
WHERE s.task_id = ? AND s.kind = 'dex_session' ORDER BY s.is_primary DESC, s.started_at, c.id;

-- name: LinkWorkTaskSession :one
-- Keep the original timestamp on replay. The first execution becomes primary.
INSERT INTO task_executions (id, task_id, conversation_id, bot_id, kind, is_primary, started_at)
SELECT 'session:' || @task_id || ':' || c.id, @task_id, c.id, c.agent_id, 'dex_session',
  NOT EXISTS (SELECT 1 FROM task_executions s WHERE s.task_id = @task_id AND s.is_primary = 1), @linked_at
FROM conversations c WHERE c.id = @conversation_id
ON CONFLICT DO NOTHING RETURNING *;

-- name: GetWorkTaskSession :one
SELECT * FROM task_executions WHERE task_id = ? AND conversation_id = ? AND kind = 'dex_session';

-- name: ClearWorkTaskPrimarySession :exec
UPDATE task_executions SET is_primary = 0 WHERE task_id = ? AND is_primary = 1;

-- name: PromoteWorkTaskSession :exec
UPDATE task_executions SET is_primary = 1 WHERE id = ?;

-- name: HasWorkTaskBotExecution :one
SELECT EXISTS (SELECT 1 FROM task_executions WHERE task_id = @task_id AND bot_id = @bot_id
  AND (CAST(@conversation_id AS INTEGER) = 0 OR (kind = 'dex_session' AND conversation_id = @conversation_id)));

-- name: HasWorkTaskExternalExecution :one
SELECT EXISTS (SELECT 1 FROM task_executions WHERE task_id = @task_id AND id = @id AND kind = 'external_agent');

-- name: ListWorkTaskTimeline :many
SELECT en.id, CAST(CASE WHEN en.trust = 'external' THEN '' ELSE en.content END AS TEXT) AS content,
  en.created_at, en.trust, en.meta_json, CAST(coalesce(en.event_id, '') AS TEXT) AS event_id,
  CAST(coalesce(ev.event_type, '') AS TEXT) AS event_type, CAST(coalesce(ev.source_url, '') AS TEXT) AS source_url,
  CAST(coalesce(ev.author_association, '') AS TEXT) AS author_association
FROM work_task_entries en LEFT JOIN work_source_events ev ON ev.id = en.event_id
WHERE en.task_id = @task_id AND en.kind IN ('note', 'artifact', 'external')
AND json_extract(en.meta_json, '$.legacyHidden') IS NOT 1
AND NOT EXISTS (SELECT 1 FROM work_task_entries r WHERE r.task_id = en.task_id AND r.kind = 'tombstone'
  AND CAST(json_extract(r.meta_json, '$.targetSeq') AS INTEGER) = en.seq)
ORDER BY en.id DESC LIMIT @max_rows;

-- name: GetWorkTaskToolOperation :one
SELECT * FROM work_task_tool_operations WHERE key = ?;

-- name: CreateWorkTaskToolOperation :exec
INSERT INTO work_task_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
VALUES (?, ?, ?, ?, ?, ?);
