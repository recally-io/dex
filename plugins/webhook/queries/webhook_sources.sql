-- name: SaveWebhookSource :execrows
INSERT INTO plugin_webhook_sources (id, secret, task_agent_id, hold_unmatched)
SELECT @id, @secret, sqlc.narg(task_agent_id), @hold_unmatched
WHERE sqlc.narg(task_agent_id) IS NULL OR EXISTS (
  SELECT 1 FROM agents a WHERE a.id = sqlc.narg(task_agent_id) AND a.archived_at IS NULL
)
ON CONFLICT (id) DO UPDATE SET secret = excluded.secret, task_agent_id = excluded.task_agent_id,
  hold_unmatched = excluded.hold_unmatched;

-- name: GetWebhookSource :one
SELECT * FROM plugin_webhook_sources WHERE id = ?;

-- name: DeleteWebhookSource :exec
DELETE FROM plugin_webhook_sources WHERE id = ?;
