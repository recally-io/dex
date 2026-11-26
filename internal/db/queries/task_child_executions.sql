-- name: ProjectTaskChildSession :exec
-- Select the first parent turn before checking settlement. A later turn cannot replace it.
UPDATE task_sessions
SET (turn_request_id, state, ended_at) = (
  SELECT s.id, CASE
    WHEN s.status = 'done' THEN 'completed'
    WHEN s.reason IN ('stopped', 'aborted', 'withdrawn', 'reset') THEN 'cancelled'
    ELSE 'failed'
  END, s.settled_at
  FROM submissions s WHERE s.session_id = task_sessions.session_id
    AND s.kind = 'message' AND s.parent_request_id IS NOT NULL ORDER BY s.rowid LIMIT 1
)
WHERE task_sessions.session_id = @session_id AND task_sessions.state = 'running'
  AND EXISTS (SELECT 1 FROM sessions c WHERE c.id = task_sessions.session_id AND c.parent_id IS NOT NULL)
  AND EXISTS (
    SELECT 1 FROM (
      SELECT s.id, s.status, s.settled_at FROM submissions s
      WHERE s.session_id = task_sessions.session_id
        AND s.kind = 'message' AND s.parent_request_id IS NOT NULL ORDER BY s.rowid LIMIT 1
    ) first_turn
    WHERE first_turn.status <> 'pending' AND first_turn.settled_at IS NOT NULL
      AND task_sessions.turn_request_id IN ('', first_turn.id)
  );
