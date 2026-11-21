-- name: CreateWorkTask :one
INSERT INTO work_tasks (id, agent_id, title, created_at, updated_at)
SELECT @id, a.id, @title, @at, @at FROM agents a WHERE a.id = @agent_id AND a.archived_at IS NULL
RETURNING *;

-- name: ListWorkTasks :many
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.agent_id
WHERE a.archived_at IS NULL AND (t.lifecycle = 'open' OR sqlc.arg(include_closed))
ORDER BY t.updated_at DESC, t.id DESC;

-- name: GetWorkTask :one
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.agent_id
WHERE t.id = @id AND t.agent_id = @agent_id AND a.archived_at IS NULL;

-- name: SetWorkTaskLifecycle :one
UPDATE work_tasks SET lifecycle = @lifecycle, updated_at = @updated_at
WHERE work_tasks.id = @id AND work_tasks.agent_id = @agent_id AND EXISTS (SELECT 1 FROM agents a WHERE a.id = @agent_id AND a.archived_at IS NULL)
RETURNING *;

-- name: InsertWorkTaskEntry :one
INSERT INTO work_task_entries (task_id, content, created_at)
SELECT t.id, @content, @created_at FROM work_tasks t JOIN agents a ON a.id = t.agent_id
WHERE t.id = @task_id AND t.agent_id = @agent_id AND a.archived_at IS NULL
RETURNING *;

-- name: TouchWorkTask :exec
UPDATE work_tasks SET updated_at = max(updated_at, CAST(@at AS INTEGER)) WHERE id = @id;

-- name: ListWorkTaskEntries :many
SELECT * FROM work_task_entries WHERE task_id = ? ORDER BY id DESC LIMIT 100;
