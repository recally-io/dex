-- name: GetFeishuSettings :one
SELECT * FROM feishu_settings WHERE agent_id = ?;

-- name: SetFeishuSettings :exec
INSERT INTO feishu_settings (agent_id, app_id, app_secret, brand, owner_id, pairing_code, pairing_expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id) DO UPDATE SET app_id = excluded.app_id, app_secret = excluded.app_secret,
brand = excluded.brand, owner_id = excluded.owner_id, pairing_code = excluded.pairing_code,
pairing_expires_at = excluded.pairing_expires_at;

-- name: PairFeishuOwner :execrows
UPDATE feishu_settings SET owner_id = ?, pairing_code = '', pairing_expires_at = 0
WHERE agent_id = ? AND owner_id = '' AND pairing_code = ? AND pairing_expires_at > ?;

-- name: DeleteFeishuSettings :exec
DELETE FROM feishu_settings WHERE agent_id = ?;
