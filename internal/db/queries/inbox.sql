-- name: ListInbox :many
SELECT r.id AS run_id, a.agent_id, b.name AS agent_name,
    a.id AS automation_id, a.name AS automation_name,
    s.conversation_id AS chat_id, r.outcome, r.summary, r.created_at,
    r.read_at, r.dismissed_at
FROM automation_runs r
JOIN automations a ON a.id = r.automation_id
JOIN agents b ON b.id = a.agent_id
JOIN submissions s ON s.id = r.id
WHERE r.outcome IN ('changed', 'failed') AND r.duplicate = 0
    AND s.status <> 'pending'
    AND (r.dismissed_at IS NULL OR sqlc.arg(include_dismissed) = 1)
ORDER BY r.created_at DESC, r.id DESC;

-- name: ReadInboxItem :execrows
UPDATE automation_runs SET read_at = coalesce(read_at, sqlc.arg(now))
WHERE automation_runs.id = sqlc.arg(id) AND outcome IN ('changed', 'failed') AND duplicate = 0
    AND EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id AND s.status <> 'pending');

-- name: DismissInboxItem :execrows
UPDATE automation_runs SET dismissed_at = coalesce(dismissed_at, sqlc.arg(now))
WHERE automation_runs.id = sqlc.arg(id) AND outcome IN ('changed', 'failed') AND duplicate = 0
    AND EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id AND s.status <> 'pending');
