-- name: GetSystemSettings :one
SELECT id, update_channel FROM system_settings WHERE id = 1;

-- name: SetUpdateChannel :exec
UPDATE system_settings SET update_channel = ? WHERE id = 1;
