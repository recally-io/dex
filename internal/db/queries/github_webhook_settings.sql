-- name: SaveGitHubWebhook :execrows
INSERT INTO github_webhook_settings (repository_id, secret, issue_agent_id)
SELECT @repository_id, @secret, sqlc.narg(issue_agent_id)
WHERE sqlc.narg(issue_agent_id) IS NULL OR EXISTS (
  SELECT 1 FROM agents a WHERE a.id = sqlc.narg(issue_agent_id) AND a.archived_at IS NULL
)
ON CONFLICT (repository_id) DO UPDATE SET secret = excluded.secret, issue_agent_id = excluded.issue_agent_id;

-- name: GetGitHubWebhook :one
SELECT * FROM github_webhook_settings WHERE repository_id = ?;

-- name: DeleteGitHubWebhook :exec
DELETE FROM github_webhook_settings WHERE repository_id = ?;
