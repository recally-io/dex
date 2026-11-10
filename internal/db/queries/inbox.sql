-- name: ListInbox :many
SELECT r.id AS run_id, a.agent_id, b.name AS agent_name,
    a.id AS automation_id, a.name AS automation_name,
    CAST(coalesce(s.conversation_id, 0) AS INTEGER) AS chat_id,
    CAST(CASE WHEN r.status = 'failed' OR s.status = 'unanswered' THEN 'failed' ELSE r.outcome END AS TEXT) AS outcome,
    CAST(CASE WHEN r.summary = '' THEN coalesce(r.reason, s.reason, '') ELSE r.summary END AS TEXT) AS summary, r.created_at,
    r.read_at, r.dismissed_at
FROM automation_runs r
JOIN automations a ON a.id = r.automation_id
JOIN agents b ON b.id = a.agent_id
LEFT JOIN submissions s ON s.id = r.id
WHERE (r.outcome = 'failed' OR r.status = 'failed' OR s.status = 'unanswered' OR (r.outcome = 'changed' AND r.duplicate = 0))
    AND (s.status <> 'pending' OR (s.id IS NULL AND r.status = 'failed'))
    AND (r.dismissed_at IS NULL OR sqlc.arg(include_dismissed) = 1)
ORDER BY r.created_at DESC, r.id DESC;

-- name: ReadInboxItem :execrows
UPDATE automation_runs SET read_at = coalesce(read_at, sqlc.arg(now))
WHERE automation_runs.id = sqlc.arg(id) AND (
    (automation_runs.status = 'failed' AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id)) OR
    EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id AND s.status <> 'pending'
        AND (outcome = 'failed' OR automation_runs.status = 'failed' OR s.status = 'unanswered' OR (outcome = 'changed' AND duplicate = 0)))
);

-- name: RestoreInboxItem :execrows
UPDATE automation_runs SET dismissed_at = NULL
WHERE automation_runs.id = sqlc.arg(id) AND (
    (automation_runs.status = 'failed' AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id)) OR
    EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id AND s.status <> 'pending'
        AND (outcome = 'failed' OR automation_runs.status = 'failed' OR s.status = 'unanswered' OR (outcome = 'changed' AND duplicate = 0)))
);

-- name: DismissInboxItem :execrows
UPDATE automation_runs SET dismissed_at = coalesce(dismissed_at, sqlc.arg(now))
WHERE automation_runs.id = sqlc.arg(id) AND (
    (automation_runs.status = 'failed' AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id)) OR
    EXISTS (SELECT 1 FROM submissions s WHERE s.id = automation_runs.id AND s.status <> 'pending'
        AND (outcome = 'failed' OR automation_runs.status = 'failed' OR s.status = 'unanswered' OR (outcome = 'changed' AND duplicate = 0)))
);
