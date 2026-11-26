-- name: SaveGitHubWebhook :execrows
INSERT INTO plugin_github_webhook_settings (repository_id, secret, issue_bot_id, hold_unmatched)
SELECT @repository_id, @secret, sqlc.narg(issue_bot_id), @hold_unmatched
WHERE sqlc.narg(issue_bot_id) IS NULL OR EXISTS (
  SELECT 1 FROM bots a WHERE a.id = sqlc.narg(issue_bot_id) AND a.archived_at IS NULL
)
ON CONFLICT (repository_id) DO UPDATE SET secret = excluded.secret, issue_bot_id = excluded.issue_bot_id,
  hold_unmatched = excluded.hold_unmatched;

-- name: GetGitHubWebhook :one
SELECT * FROM plugin_github_webhook_settings WHERE repository_id = ?;

-- name: DeleteGitHubWebhook :exec
DELETE FROM plugin_github_webhook_settings WHERE repository_id = ?;
