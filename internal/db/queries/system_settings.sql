-- name: GetSystemSettings :one
SELECT id, update_channel FROM system_settings WHERE id = 1;

-- name: SetUpdateChannel :exec
INSERT INTO system_settings (id, update_channel)
VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET update_channel = excluded.update_channel;
