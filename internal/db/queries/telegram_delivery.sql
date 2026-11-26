-- name: SetTelegramRetryAt :exec
UPDATE telegram_settings SET retry_at = max(retry_at, @retry_at)
WHERE bot_id = @bot_id AND token = @token;
