-- name: TelegramManagerCredential :one
SELECT credential_ref FROM telegram_manager WHERE id = 1;

-- name: SetTelegramManagerCredential :exec
INSERT INTO telegram_manager (id, credential_ref) VALUES (1, ?)
ON CONFLICT (id) DO UPDATE SET credential_ref = excluded.credential_ref;

-- name: DeleteTelegramManagerCredential :exec
DELETE FROM telegram_manager WHERE id = 1;

-- name: CancelAllTelegramManagedSetups :exec
UPDATE telegram_managed_setups SET state = 'canceled' WHERE state = 'pending';

-- name: CreateTelegramManagedSetup :one
INSERT INTO telegram_managed_setups (id, bot_id, manager_id, username, expires_at)
VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: GetTelegramManagedSetup :one
SELECT * FROM telegram_managed_setups WHERE id = ? AND bot_id = ?;

-- name: CancelTelegramManagedSetup :exec
UPDATE telegram_managed_setups SET state = 'canceled'
WHERE id = ? AND bot_id = ? AND state = 'pending';

-- name: CancelBotTelegramManagedSetups :exec
UPDATE telegram_managed_setups SET state = 'canceled' WHERE bot_id = ? AND state = 'pending';

-- name: PruneTelegramManagedSetups :exec
DELETE FROM telegram_managed_setups WHERE telegram_bot_id IS NULL AND expires_at <= ?;

-- name: PairTelegramManagedOwner :exec
UPDATE telegram_managed_setups SET owner_id = @owner_id
WHERE telegram_managed_setups.id = @id AND manager_id = @manager_id AND owner_id = 0
  AND state = 'pending' AND expires_at > @now
  AND EXISTS (SELECT 1 FROM bots WHERE bots.id = telegram_managed_setups.bot_id AND bots.archived_at IS NULL);

-- name: ClaimTelegramManagedBot :one
UPDATE telegram_managed_setups SET telegram_bot_id = @telegram_bot_id
WHERE telegram_managed_setups.username = @username AND telegram_managed_setups.manager_id = @manager_id
  AND telegram_managed_setups.owner_id = @owner_id AND telegram_managed_setups.state = 'pending'
  AND telegram_managed_setups.expires_at > @now AND (telegram_managed_setups.telegram_bot_id IS NULL OR telegram_managed_setups.telegram_bot_id = @telegram_bot_id)
  AND EXISTS (SELECT 1 FROM bots WHERE bots.id = telegram_managed_setups.bot_id AND bots.archived_at IS NULL)
  AND NOT EXISTS (SELECT 1 FROM telegram_managed_setups AS other
                  WHERE other.telegram_bot_id = @telegram_bot_id AND other.username <> @username)
RETURNING *;

-- name: FinishTelegramManagedSetup :execrows
UPDATE telegram_managed_setups SET state = @state
WHERE telegram_managed_setups.id = @id AND state = 'pending' AND expires_at > @now
  AND EXISTS (SELECT 1 FROM bots WHERE bots.id = telegram_managed_setups.bot_id AND bots.archived_at IS NULL);
