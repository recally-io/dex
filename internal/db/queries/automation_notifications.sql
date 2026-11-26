-- name: InsertAutomationNotificationTarget :exec
INSERT INTO automation_notification_targets (id, bot_id, platform, account_id, peer)
VALUES (?, ?, ?, ?, ?);

-- name: ListAutomationNotificationTargets :many
SELECT * FROM automation_notification_targets WHERE bot_id = ? ORDER BY platform, peer, id;

-- name: GetAutomationNotificationTarget :one
SELECT * FROM automation_notification_targets WHERE id = ? AND bot_id = ?;

-- name: GetPrimaryNotificationTarget :one
SELECT t.* FROM bots a JOIN automation_notification_targets t
ON t.id = a.primary_notification_target_id AND t.bot_id = a.id
WHERE a.id = ? AND a.archived_at IS NULL;

-- name: SetPrimaryNotificationTarget :execrows
UPDATE bots SET primary_notification_target_id = ? WHERE id = ?;

-- name: GetAutomationOutboxInfo :one
SELECT a.name, a.bot_id, t.account_id, t.peer
FROM automation_runs r
JOIN automations a ON a.id = r.automation_id
JOIN bots bot ON bot.id = a.bot_id
JOIN automation_notification_targets t ON t.bot_id = a.bot_id AND t.id =
    CASE a.notification_route WHEN 'default' THEN bot.primary_notification_target_id WHEN 'target' THEN a.notification_target_id END
JOIN configured_channel_addresses b ON b.bot_id = t.bot_id AND b.platform = t.platform
    AND b.account_id = t.account_id AND b.peer = t.peer
WHERE r.id = ? AND bot.archived_at IS NULL;

-- name: GetAutomationOutboxSession :one
-- An owner for the Outbox FK only. This is not a source input or delivery route.
SELECT id FROM sessions
WHERE bot_id = ? AND archived_at IS NULL AND parent_id IS NULL
ORDER BY (channel_key = 'main') DESC, id
LIMIT 1;
