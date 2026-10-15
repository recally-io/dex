-- name: GetWechatSettings :one
SELECT * FROM wechat_settings WHERE id = 1;

-- name: SetWechatSettings :exec
INSERT INTO wechat_settings (id, token, base_url, bot_id, owner) VALUES (1, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET token = excluded.token, base_url = excluded.base_url,
bot_id = excluded.bot_id, owner = excluded.owner;

-- name: DeleteWechatSettings :exec
DELETE FROM wechat_settings WHERE id = 1;

-- name: EnsureWechatRuntime :one
INSERT INTO wechat_runtime (bot_id, owner) VALUES (?, ?)
ON CONFLICT (bot_id, owner) DO UPDATE SET bot_id = excluded.bot_id RETURNING *;

-- name: GetWechatRuntime :one
SELECT * FROM wechat_runtime WHERE bot_id = ? AND owner = ?;

-- name: SetWechatCursor :exec
UPDATE wechat_runtime SET cursor = ? WHERE bot_id = ? AND owner = ?;

-- name: SetWechatContext :exec
UPDATE wechat_runtime SET context_token = @context_token, context_message_id = @message_id
WHERE bot_id = @bot_id AND owner = @owner AND context_message_id <= @message_id;

-- name: SetWechatPause :exec
UPDATE wechat_runtime SET pause_until = ? WHERE bot_id = ? AND owner = ?;
