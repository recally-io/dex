-- name: InsertRoutineNotificationTarget :exec
INSERT INTO routine_notification_targets (id, agent_id, platform, account_id, peer)
VALUES (?, ?, ?, ?, ?);

-- name: ListRoutineNotificationTargets :many
SELECT * FROM routine_notification_targets WHERE agent_id = ? ORDER BY platform, peer, id;

-- name: GetRoutineNotificationTarget :one
SELECT * FROM routine_notification_targets WHERE id = ? AND agent_id = ?;

-- name: SetPrimaryNotificationTarget :execrows
UPDATE agents SET primary_notification_target_id = ? WHERE id = ?;

-- name: QueueRoutineNotification :exec
INSERT INTO routine_notification_deliveries (run_id, agent_id, target_id, platform, account_id, peer, body)
SELECT r.id, a.agent_id, t.id, t.platform, t.account_id, t.peer,
       a.name || ': ' || r.outcome || char(10) || CASE WHEN r.summary != '' THEN r.summary ELSE coalesce(r.reason, '') END
FROM automation_runs r
JOIN automations a ON a.id = r.automation_id
JOIN agents agent ON agent.id = a.agent_id
JOIN routine_notification_targets t ON t.agent_id = a.agent_id AND t.id =
    CASE a.notification_route WHEN 'default' THEN agent.primary_notification_target_id WHEN 'target' THEN a.notification_target_id END
WHERE r.id = ? AND r.outcome IN ('changed', 'unchanged', 'failed')
ON CONFLICT(run_id) DO NOTHING;

-- name: GetRoutineNotification :one
SELECT * FROM routine_notification_deliveries WHERE run_id = ?;

-- name: ListRoutineNotifications :many
SELECT * FROM routine_notification_deliveries
WHERE agent_id = ? AND platform = ? AND status = 'pending' ORDER BY rowid;

-- name: SetRoutineNotificationParts :exec
UPDATE routine_notification_deliveries SET rendered_parts = ? WHERE run_id = ? AND status = 'pending' AND rendered_parts = '';

-- name: AdvanceRoutineNotification :exec
UPDATE routine_notification_deliveries SET parts = parts + 1 WHERE run_id = ? AND status = 'pending';

-- name: SetRoutineNotificationStatus :exec
UPDATE routine_notification_deliveries SET status = ?, reason = ? WHERE run_id = ? AND status = 'pending';
