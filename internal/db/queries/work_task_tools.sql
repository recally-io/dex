-- name: ListBotWorkTasks :many
-- Match CanRead for Bot actors before applying the limit.
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE a.archived_at IS NULL AND (
  (CAST(@child_conversation_id AS INTEGER) = 0 AND
    (t.assignee_bot_id = @bot_id OR (t.reviewer_kind = 'bot' AND t.reviewer_id = @bot_id)))
  OR EXISTS (SELECT 1 FROM task_executions e WHERE e.task_id = t.id AND e.bot_id = @bot_id
    AND e.kind = 'dex_session' AND e.conversation_id = @child_conversation_id)
)
AND (CAST(@status_filter AS TEXT) = 'all' OR t.status = @status_filter
  OR (@status_filter = 'open' AND t.status NOT IN ('done', 'cancelled')))
ORDER BY t.updated_at DESC, t.id DESC LIMIT 50;

-- name: GetBoardTaskByNumber :one
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE t.number = ? AND a.archived_at IS NULL;

-- name: HasWorkTaskTitleEntry :one
SELECT EXISTS (SELECT 1 FROM work_task_entries
  WHERE task_id = @task_id AND kind = 'status' AND trust <> 'external' AND content = @title);

-- name: ListWorkTaskSessions :many
SELECT c.id AS conversation_id, s.bot_id AS agent_id, a.name AS agent_name, c.title, s.is_primary, s.started_at AS linked_at, s.state, s.ended_at
FROM task_executions s JOIN conversations c ON c.id = s.conversation_id JOIN agents a ON a.id = s.bot_id
WHERE s.task_id = ? AND s.kind = 'dex_session' ORDER BY s.is_primary DESC, s.started_at, c.id;

-- name: LinkWorkTaskSession :one
-- Keep the original timestamp and child turn on replay. The first execution becomes primary.
INSERT INTO task_executions (id, task_id, conversation_id, bot_id, kind, is_primary, started_at, turn_request_id)
SELECT 'session:' || @task_id || ':' || c.id, @task_id, c.id, c.agent_id, 'dex_session',
  NOT EXISTS (SELECT 1 FROM task_executions s WHERE s.task_id = @task_id AND s.is_primary = 1), @linked_at,
  COALESCE((SELECT s.id FROM submissions s WHERE s.conversation_id = c.id AND s.kind = 'message'
    AND s.parent_request_id IS NOT NULL AND c.parent_id IS NOT NULL ORDER BY s.rowid LIMIT 1), '')
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
  AND kind = 'dex_session' AND conversation_id = CAST(@conversation_id AS INTEGER));

-- name: HasWorkTaskExternalExecution :one
SELECT EXISTS (SELECT 1 FROM task_executions WHERE task_id = @task_id AND id = @id AND kind = 'external_agent');

-- name: ListWorkTaskTimeline :many
SELECT en.id, en.seq, en.kind, en.tag, en.actor_kind, en.actor_bot_id, en.actor_execution_id,
  CAST(CASE WHEN en.trust = 'external' THEN '' ELSE en.content END AS TEXT) AS content,
  en.created_at, en.trust,
  CAST(coalesce(ev.event_type, '') AS TEXT) AS event_type, CAST(coalesce(ev.source_url, '') AS TEXT) AS source_url,
  CAST(coalesce(ev.author_association, '') AS TEXT) AS author_association,
  CAST(CASE WHEN CAST(@bootstrap AS INTEGER) = 1 AND en.kind = 'note' AND en.tag IN ('brief', 'decision') THEN 0 ELSE 1 END AS INTEGER) AS priority,
  CAST(CASE WHEN CAST(@bootstrap AS INTEGER) = 1 THEN -en.seq ELSE en.seq END AS INTEGER) AS sort_seq
FROM work_task_entries en LEFT JOIN work_source_events ev ON ev.id = en.event_id
WHERE en.task_id = @task_id AND en.seq > @after_seq AND en.kind <> 'tombstone'
AND (json_array_length(CAST(@tags AS TEXT)) = 0 OR en.tag IN (SELECT value FROM json_each(@tags)))
AND NOT EXISTS (SELECT 1 FROM work_task_entries r WHERE r.task_id = en.task_id AND r.kind = 'tombstone'
  AND CAST(json_extract(r.meta_json, '$.targetSeq') AS INTEGER) = en.seq)
ORDER BY priority, sort_seq
LIMIT @max_rows;

-- name: GetWorkTaskToolOperation :one
SELECT * FROM work_task_tool_operations WHERE key = ?;

-- name: CreateWorkTaskToolOperation :exec
INSERT INTO work_task_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
VALUES (?, ?, ?, ?, ?, ?);
