-- name: GetRoutineToolOperation :one
SELECT * FROM routine_tool_operations WHERE key = ?;

-- name: CreateRoutineToolOperation :exec
INSERT INTO routine_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
VALUES (?, ?, ?, ?, ?, ?);

-- name: LockRoutineTools :exec
UPDATE routine_tool_operations SET key = key WHERE 0;
