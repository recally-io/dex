-- name: GetActiveArtifactInput :one
SELECT id FROM submissions WHERE id = ? AND conversation_id = ? AND status = 'pending';

-- name: LockArtifactInput :execrows
UPDATE submissions SET id = id WHERE id = ? AND conversation_id = ? AND status = 'pending';

-- name: CreateArtifact :exec
INSERT INTO artifacts (id, agent_id, conversation_id, display_conversation_id, operation_key, name, size, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetArtifactByOperation :one
SELECT * FROM artifacts WHERE operation_key = ? AND agent_id = ?;

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = ? AND agent_id = ? AND (conversation_id = ? OR display_conversation_id = ?);

-- name: ListArtifacts :many
SELECT * FROM artifacts WHERE agent_id = ? AND (conversation_id = ? OR display_conversation_id = ?) ORDER BY created_at, id;
