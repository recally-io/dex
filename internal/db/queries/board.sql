-- name: ListBoardTasks :many
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE a.archived_at IS NULL
  AND (t.status NOT IN ('done', 'cancelled') OR @include_closed)
  AND (CAST(@bot_id AS TEXT) = '' OR t.assignee_bot_id = @bot_id)
  AND (CAST(@status_filter AS TEXT) = '' OR t.status = @status_filter)
  AND (sqlc.narg(in_review) IS NULL OR (t.status = 'in_review') = CAST(sqlc.narg(in_review) AS INTEGER))
  AND (CAST(@source AS TEXT) = '' OR EXISTS (
    SELECT 1 FROM work_task_objects o WHERE o.task_id = t.id
    AND (o.namespace = @source OR instr(o.namespace, @source || '/') = 1)
  ))
ORDER BY t.updated_at DESC, t.id DESC;

-- name: UpdateBoardTaskRoles :one
UPDATE work_tasks SET assignee_bot_id = @assignee_bot_id, reviewer_kind = @reviewer_kind, reviewer_id = @reviewer_id
WHERE id = @id RETURNING *;

-- name: ListBoardTimeline :many
SELECT e.* FROM work_task_entries e
WHERE e.task_id = @task_id AND e.kind <> 'tombstone'
AND NOT EXISTS (SELECT 1 FROM work_task_entries r WHERE r.task_id = e.task_id AND r.kind = 'tombstone'
  AND CAST(json_extract(r.meta_json, '$.targetSeq') AS INTEGER) = e.seq)
ORDER BY e.seq;

-- name: BoardEventHead :one
SELECT CAST(coalesce(max(id), 0) AS INTEGER) FROM work_task_entries;

-- name: ListBoardEvents :many
SELECT id, task_id FROM work_task_entries WHERE id > @after_id ORDER BY id LIMIT 128;

-- name: HasBoardConversation :one
SELECT EXISTS (
  SELECT 1 FROM task_executions e WHERE e.conversation_id = sqlc.arg(chat_id)
  UNION ALL
  SELECT 1 FROM submissions s JOIN work_task_inputs i ON i.submission_id = s.id
  WHERE s.conversation_id = sqlc.arg(chat_id)
);
