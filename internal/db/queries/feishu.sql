-- name: GetFeishuSettings :one
SELECT * FROM feishu_settings WHERE bot_id = ?;

-- name: SetFeishuSettings :exec
INSERT INTO feishu_settings (bot_id, app_id, app_secret, brand, owner_id, pairing_code, pairing_expires_at, reactions, verification_token, encrypt_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (bot_id) DO UPDATE SET app_id = excluded.app_id, app_secret = excluded.app_secret,
brand = excluded.brand, owner_id = excluded.owner_id, pairing_code = excluded.pairing_code,
pairing_expires_at = excluded.pairing_expires_at, reactions = excluded.reactions,
verification_token = excluded.verification_token, encrypt_key = excluded.encrypt_key;

-- name: PairFeishuOwner :execrows
UPDATE feishu_settings SET owner_id = ?, pairing_code = '', pairing_expires_at = 0
WHERE bot_id = ? AND owner_id = '' AND pairing_code = ? AND pairing_expires_at > ?;

-- name: DeleteFeishuSettings :exec
DELETE FROM feishu_settings WHERE bot_id = ?;
