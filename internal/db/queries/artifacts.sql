-- name: GetActiveArtifactInput :one
SELECT id FROM submissions WHERE id = ? AND conversation_id = ? AND status = 'pending';

-- name: LockArtifactInput :execrows
UPDATE submissions SET id = id WHERE id = ? AND conversation_id = ? AND status = 'pending';

-- name: CreateArtifact :exec
INSERT INTO artifacts (id, agent_id, conversation_id, display_conversation_id, operation_key, name, size, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetArtifactByOperation :one
SELECT * FROM artifacts WHERE operation_key = ? AND agent_id = ?;

-- The caller has already checked access to the conversation. The file can belong to another bot:
-- a child in another bot shows its file in the conversation of its parent. artifacts.agent_id
-- names the bot that holds the file.
-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = ? AND deleted = 0 AND (conversation_id = ? OR display_conversation_id = ?);

-- name: ListArtifacts :many
SELECT * FROM artifacts WHERE deleted = 0 AND (conversation_id = ? OR display_conversation_id = ?) ORDER BY created_at, id;

-- name: RenameArtifact :one
UPDATE artifacts SET name = ? WHERE id = ? AND deleted = 0 AND (conversation_id = ? OR display_conversation_id = ?) RETURNING *;

-- Keep the operation key so a tool replay cannot restore a deleted delivery.
-- name: DeleteArtifact :one
UPDATE artifacts SET deleted = 1 WHERE id = ? AND (conversation_id = ? OR display_conversation_id = ?) RETURNING *;
