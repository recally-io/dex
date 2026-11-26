-- name: GetSessionByExecution :one
SELECT * FROM sessions WHERE agent_id = ? AND channel_key NOT LIKE 'retired-telegram:%' AND execution_id = ?;

-- name: ListSessionsForStop :many
-- Stop reaches the session and the children of it that have pending input that the parent
-- started. The children can belong to different bots.
-- A child with only user input is not reached.
SELECT * FROM sessions
WHERE channel_key NOT LIKE 'retired-telegram:%' AND (
    (sessions.id = @session_id AND sessions.agent_id = @agent_id)
    OR (sessions.parent_id = @session_id AND EXISTS (
    SELECT 1 FROM submissions s WHERE s.session_id = sessions.id AND s.status = 'pending'
        AND s.parent_request_id IS NOT NULL
)))
ORDER BY sessions.id;

-- name: ListChildrenStoppedByParent :many
-- The children that Stop on their parent reaches. Keep the rule equal to ListSessionsForStop: the
-- web UI shows Stop on a parent page only for these.
SELECT sessions.id FROM sessions
JOIN sessions parent ON parent.id = sessions.parent_id
WHERE (sessions.agent_id = @agent_id OR parent.agent_id = @agent_id)
AND sessions.channel_key NOT LIKE 'retired-telegram:%' AND parent.channel_key NOT LIKE 'retired-telegram:%' AND EXISTS (
    SELECT 1 FROM submissions s WHERE s.session_id = sessions.id AND s.status = 'pending'
        AND s.parent_request_id IS NOT NULL
);

-- name: StopSessionSubmissions :execrows
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE session_id = @session_id AND status = 'pending';

-- name: StopSubmission :execrows
-- Stop ends one pending input that native execution withdrew.
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE id = @id AND status = 'pending';
