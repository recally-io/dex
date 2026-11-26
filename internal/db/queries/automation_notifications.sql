-- name: InsertAutomationNotificationTarget :exec
INSERT INTO automation_notification_targets (id, agent_id, platform, account_id, peer)
VALUES (?, ?, ?, ?, ?);

-- name: ListAutomationNotificationTargets :many
SELECT * FROM automation_notification_targets WHERE agent_id = ? ORDER BY platform, peer, id;

-- name: GetAutomationNotificationTarget :one
SELECT * FROM automation_notification_targets WHERE id = ? AND agent_id = ?;

-- name: GetPrimaryNotificationTarget :one
SELECT t.* FROM agents a JOIN automation_notification_targets t
ON t.id = a.primary_notification_target_id AND t.agent_id = a.id
WHERE a.id = ? AND a.archived_at IS NULL;

-- name: SetPrimaryNotificationTarget :execrows
UPDATE agents SET primary_notification_target_id = ? WHERE id = ?;

-- name: GetAutomationOutboxInfo :one
SELECT a.name, a.agent_id, t.account_id, t.peer
FROM automation_runs r
JOIN automations a ON a.id = r.automation_id
JOIN agents agent ON agent.id = a.agent_id
JOIN automation_notification_targets t ON t.agent_id = a.agent_id AND t.id =
    CASE a.notification_route WHEN 'default' THEN agent.primary_notification_target_id WHEN 'target' THEN a.notification_target_id END
JOIN configured_channel_addresses b ON b.agent_id = t.agent_id AND b.platform = t.platform
    AND b.account_id = t.account_id AND b.peer = t.peer
WHERE r.id = ? AND agent.archived_at IS NULL;

-- name: GetAutomationOutboxSession :one
-- An owner for the Outbox FK only. This is not a source input or delivery route.
SELECT id FROM sessions
WHERE agent_id = ? AND archived_at IS NULL AND parent_id IS NULL
ORDER BY (channel_key = 'main') DESC, id
LIMIT 1;
