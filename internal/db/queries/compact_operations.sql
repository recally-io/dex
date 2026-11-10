-- name: GetCompactOperation :one
SELECT * FROM compact_operations WHERE operation_id = ?;

-- name: CreateCompactOperation :exec
INSERT INTO compact_operations (operation_id, agent_id, conversation_id, status, created_at)
VALUES (?, ?, ?, ?, ?);

-- name: ListPendingCompactOperations :many
SELECT * FROM compact_operations WHERE status = 'pending' ORDER BY rowid;

-- name: AdmitCompactOperation :exec
UPDATE compact_operations SET status = 'admitted', task_id = ? WHERE operation_id = ? AND status = 'pending';
