-- name: ListTasks :many
SELECT t.* FROM tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE a.archived_at IS NULL
  AND (t.status NOT IN ('done', 'cancelled') OR @include_closed)
  AND (CAST(@bot_id AS TEXT) = '' OR t.assignee_bot_id = @bot_id)
  AND (CAST(@status_filter AS TEXT) = '' OR t.status = @status_filter)
  AND (sqlc.narg(in_review) IS NULL OR (t.status = 'in_review') = CAST(sqlc.narg(in_review) AS INTEGER))
  AND (CAST(@source AS TEXT) = '' OR EXISTS (
    SELECT 1 FROM task_objects o WHERE o.task_id = t.id
    AND (o.namespace = @source OR instr(o.namespace, @source || '/') = 1)
  ))
ORDER BY t.updated_at DESC, t.id DESC;

-- name: UpdateTaskRoles :one
UPDATE tasks SET assignee_bot_id = @assignee_bot_id, reviewer_kind = @reviewer_kind, reviewer_id = @reviewer_id
WHERE id = @id RETURNING *;

-- name: ListTaskEntries :many
SELECT e.* FROM task_entries e
WHERE e.task_id = @task_id AND e.kind <> 'tombstone'
AND NOT EXISTS (SELECT 1 FROM task_entries r WHERE r.task_id = e.task_id AND r.kind = 'tombstone'
  AND CAST(json_extract(r.meta_json, '$.targetSeq') AS INTEGER) = e.seq)
ORDER BY e.seq;

-- name: TaskEventHead :one
SELECT CAST(coalesce(max(id), 0) AS INTEGER) FROM task_entries;

-- name: ListTaskEvents :many
SELECT id, task_id FROM task_entries WHERE id > @after_id ORDER BY id LIMIT 128;

-- name: HasTaskConversation :one
SELECT EXISTS (
  SELECT 1 FROM task_sessions s WHERE s.conversation_id = sqlc.arg(chat_id)
  UNION ALL
  SELECT 1 FROM submissions s JOIN task_inputs i ON i.submission_id = s.id
  WHERE s.conversation_id = sqlc.arg(chat_id)
);
