-- name: RecordMainTurnStop :exec
INSERT INTO main_turn_stops (conversation_id, target) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: ListMainTurnStopAgents :many
SELECT DISTINCT c.agent_id FROM main_turn_stops s JOIN conversations c ON c.id = s.conversation_id;

-- name: ListAgentMainTurnStops :many
SELECT s.target, c.id AS conversation_id, c.execution_id, c.execution_scope
FROM main_turn_stops s JOIN conversations c ON c.id = s.conversation_id
WHERE c.agent_id = ? ORDER BY s.rowid;

-- name: FinishMainTurnStop :exec
DELETE FROM main_turn_stops WHERE conversation_id = ? AND target = ?;

-- name: SupersedeMainTurnStops :exec
DELETE FROM main_turn_stops WHERE conversation_id = ?;
