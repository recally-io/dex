-- name: SaveEmailSource :execrows
INSERT INTO email_sources (id, secret, task_agent_id, hold_unmatched)
SELECT @id, @secret, sqlc.narg(task_agent_id), @hold_unmatched
WHERE sqlc.narg(task_agent_id) IS NULL OR EXISTS (
  SELECT 1 FROM agents a WHERE a.id = sqlc.narg(task_agent_id) AND a.archived_at IS NULL
)
ON CONFLICT (id) DO UPDATE SET secret = excluded.secret, task_agent_id = excluded.task_agent_id,
  hold_unmatched = excluded.hold_unmatched;

-- name: GetEmailSource :one
SELECT * FROM email_sources WHERE id = ?;

-- name: DeleteEmailSource :exec
DELETE FROM email_sources WHERE id = ?;
