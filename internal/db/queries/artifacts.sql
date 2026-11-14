-- name: GetActiveArtifactInput :one
SELECT id FROM submissions WHERE id = ? AND conversation_id = ? AND status = 'pending';

-- name: LockArtifactInput :execrows
UPDATE submissions SET id = id WHERE id = ? AND conversation_id = ? AND status = 'pending';

-- Only the startup disk importer reads the legacy table. New deliveries never write it.
-- name: ListLegacyArtifacts :many
SELECT id, agent_id, conversation_id, display_conversation_id, operation_key, name, size, created_at FROM artifacts ORDER BY created_at, id;
