-- name: SaveEmailSource :execrows
INSERT INTO plugin_email_sources (id, secret, task_agent_id, hold_unmatched)
SELECT @id, @secret, sqlc.narg(task_agent_id), @hold_unmatched
WHERE sqlc.narg(task_agent_id) IS NULL OR EXISTS (
  SELECT 1 FROM agents a WHERE a.id = sqlc.narg(task_agent_id) AND a.archived_at IS NULL
)
ON CONFLICT (id) DO UPDATE SET secret = excluded.secret, task_agent_id = excluded.task_agent_id,
  hold_unmatched = excluded.hold_unmatched;

-- name: GetEmailSource :one
SELECT * FROM plugin_email_sources WHERE id = ?;

-- name: DeleteEmailSource :exec
DELETE FROM plugin_email_sources WHERE id = ?;

-- name: SaveEmailIMAPPull :execrows
-- A new configuration starts a new cursor.
INSERT INTO plugin_email_imap_pulls (source_id, address, username, password, folder, configuration_id)
SELECT s.id, @address, @username, @password, @folder, @configuration_id FROM plugin_email_sources s WHERE s.id = @source_id
ON CONFLICT (source_id) DO UPDATE SET address = excluded.address, username = excluded.username,
  password = excluded.password, folder = excluded.folder, configuration_id = excluded.configuration_id,
  uid_validity = 0, last_uid = 0;

-- name: DeleteEmailIMAPPull :exec
DELETE FROM plugin_email_imap_pulls WHERE source_id = ?;

-- name: ListEmailIMAPPulls :many
SELECT p.source_id, p.address, p.username, p.password, p.folder, p.configuration_id, p.uid_validity, p.last_uid,
  s.task_agent_id, s.hold_unmatched
FROM plugin_email_imap_pulls p JOIN plugin_email_sources s ON s.id = p.source_id ORDER BY p.source_id;

-- name: SetEmailIMAPCursor :exec
-- A cursor of a replaced configuration does not apply.
UPDATE plugin_email_imap_pulls SET uid_validity = @uid_validity, last_uid = @last_uid
WHERE source_id = @source_id AND configuration_id = @configuration_id;
