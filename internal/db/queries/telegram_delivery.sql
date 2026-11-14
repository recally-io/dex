-- name: SetTelegramRetryAt :exec
UPDATE telegram_settings SET retry_at = max(retry_at, @retry_at)
WHERE agent_id = @agent_id AND token = @token;
