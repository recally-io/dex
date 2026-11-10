-- name: CreateTaskSteerReceipt :one
INSERT INTO task_steer_receipts (agent_id, parent_conversation_id, task_id, request_id, native_request_id, content, state, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'pending', ?) RETURNING *;

-- name: GetTaskSteerReceipt :one
SELECT * FROM task_steer_receipts WHERE agent_id = ? AND request_id = ?;

-- name: HasTaskSteer :one
SELECT EXISTS (SELECT 1 FROM task_steer_receipts WHERE task_id = ?);

-- name: ListUndeliveredTaskSteerReasons :many
SELECT DISTINCT reason FROM task_steer_receipts
WHERE task_id = ? AND state = 'undelivered' ORDER BY reason;

-- name: FinishUnsentTaskSteers :exec
UPDATE task_steer_receipts SET state = 'undelivered', reason = ?
WHERE task_id = ? AND state = 'pending';
