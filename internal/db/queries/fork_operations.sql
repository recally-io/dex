-- name: GetForkOperation :one
SELECT * FROM fork_operations WHERE operation_id = ?;

-- name: CreateForkOperation :exec
INSERT INTO fork_operations (operation_id, agent_id, source_conversation_id, target_conversation_id, entry_id, options_json, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'pending', ?);

-- name: ListPendingForkOperations :many
SELECT * FROM fork_operations WHERE status = 'pending' ORDER BY rowid;

-- name: SetForkOperationStatus :exec
UPDATE fork_operations SET status = ? WHERE operation_id = ? AND status = 'pending';
