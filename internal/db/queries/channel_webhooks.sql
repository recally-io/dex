-- name: EnqueueChannelWebhook :exec
INSERT INTO channel_webhook_queue (platform, account, event_id, bot_id, received_at, payload)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (platform, account, event_id) DO NOTHING;

-- name: NextChannelWebhook :one
SELECT id, payload FROM channel_webhook_queue
WHERE bot_id = ? AND platform = ? AND account = ? AND payload IS NOT NULL
ORDER BY id LIMIT 1;

-- name: FinishChannelWebhook :exec
UPDATE channel_webhook_queue SET payload = NULL WHERE id = ?;

-- name: CountHandledChannelWebhook :one
SELECT count(*) FROM channel_webhook_queue
WHERE platform = ? AND account = ? AND event_id = ? AND payload IS NULL;

-- name: PruneHandledChannelWebhooks :exec
DELETE FROM channel_webhook_queue WHERE payload IS NULL AND received_at < ?;
