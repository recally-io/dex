-- name: EnqueueChannelWebhook :exec
INSERT INTO channel_webhook_inbox (platform, account, event_id, agent_id, payload)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (platform, account, event_id) DO NOTHING;

-- name: NextChannelWebhook :one
SELECT id, payload FROM channel_webhook_inbox
WHERE agent_id = ? AND platform = ? AND account = ? AND payload IS NOT NULL
ORDER BY id LIMIT 1;

-- name: FinishChannelWebhook :exec
UPDATE channel_webhook_inbox SET payload = NULL WHERE id = ?;

-- name: CountHandledChannelWebhook :one
SELECT count(*) FROM channel_webhook_inbox
WHERE platform = ? AND account = ? AND event_id = ? AND payload IS NULL;
