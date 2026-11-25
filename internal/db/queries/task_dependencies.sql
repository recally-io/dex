-- name: GetRelatedWorkTask :one
SELECT * FROM work_tasks WHERE id = ?;

-- name: GetRelatedWorkTaskByNumber :one
SELECT * FROM work_tasks WHERE number = ?;

-- name: ListWorkSubtasks :many
SELECT * FROM work_tasks WHERE parent_task_id = sqlc.narg(parent_task_id) ORDER BY number;

-- name: ListWorkTaskDependencies :many
SELECT t.* FROM task_dependencies d JOIN work_tasks t ON t.id = d.depends_on_task_id
WHERE d.task_id = ? ORDER BY t.number;

-- name: ListWorkTaskDependents :many
SELECT t.* FROM task_dependencies d JOIN work_tasks t ON t.id = d.task_id
WHERE d.depends_on_task_id = ? AND t.status NOT IN ('done', 'cancelled') ORDER BY t.number;

-- name: WorkTaskDependencyWouldCycle :one
WITH RECURSIVE upstream(id) AS (
  SELECT CAST(@depends_on_task_id AS TEXT)
  UNION
  SELECT d.depends_on_task_id FROM task_dependencies d JOIN upstream u ON u.id = d.task_id
)
SELECT EXISTS (SELECT 1 FROM upstream WHERE id = CAST(@task_id AS TEXT));

-- name: InsertWorkTaskDependency :execrows
INSERT INTO task_dependencies (task_id, depends_on_task_id, created_at) VALUES (?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: DeleteWorkTaskDependency :execrows
DELETE FROM task_dependencies WHERE task_id = ? AND depends_on_task_id = ?;

-- name: ListOwnerWorkTaskReviews :many
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE a.archived_at IS NULL AND t.status = 'in_review' AND t.reviewer_kind = 'owner'
ORDER BY t.updated_at DESC, t.number DESC;

-- name: HasBoundWorkTaskSession :one
SELECT EXISTS (SELECT 1 FROM task_executions WHERE kind = 'dex_session' AND conversation_id = ? AND bot_id = ?);
