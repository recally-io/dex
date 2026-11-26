-- name: GetAutomationToolOperation :one
SELECT * FROM automation_tool_operations WHERE key = ?;

-- name: CreateAutomationToolOperation :exec
INSERT INTO automation_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
VALUES (?, ?, ?, ?, ?, ?);

-- name: LockAutomationTools :exec
UPDATE automation_tool_operations SET key = key WHERE 0;
