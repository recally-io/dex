-- name: RecordMainTurnStop :exec
INSERT INTO main_turn_stops (session_id, target) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: ListMainTurnStops :many
SELECT target FROM main_turn_stops WHERE session_id = ? ORDER BY rowid;

-- name: FinishMainTurnStop :exec
DELETE FROM main_turn_stops WHERE session_id = ? AND target = ?;

-- name: SupersedeMainTurnStops :exec
DELETE FROM main_turn_stops WHERE session_id = ?;
