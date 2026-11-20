-- name: SaveGitHubWebhook :exec
INSERT INTO github_webhook_settings (repository_id, secret) VALUES (?, ?)
ON CONFLICT (repository_id) DO UPDATE SET secret = excluded.secret;

-- name: GetGitHubWebhookSecret :one
SELECT secret FROM github_webhook_settings WHERE repository_id = ?;

-- name: DeleteGitHubWebhook :exec
DELETE FROM github_webhook_settings WHERE repository_id = ?;
