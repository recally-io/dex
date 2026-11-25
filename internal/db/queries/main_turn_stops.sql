-- name: RecordMainTurnStop :exec
INSERT INTO main_turn_stops (conversation_id, target) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: ListMainTurnStops :many
SELECT target FROM main_turn_stops WHERE conversation_id = ? ORDER BY rowid;

-- name: FinishMainTurnStop :exec
DELETE FROM main_turn_stops WHERE conversation_id = ? AND target = ?;

-- name: SupersedeMainTurnStops :exec
DELETE FROM main_turn_stops WHERE conversation_id = ?;
