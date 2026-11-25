-- name: GetActiveChannelBinding :one
SELECT * FROM channel_bindings
WHERE agent_id = @agent_id AND peer = @peer AND owner_peer = @owner_peer AND account_id = @account_id AND revoked_at IS NULL;

-- name: EnsureTelegramReplyBinding :exec
-- Telegram supplies the private chat ID and the authorized sender ID separately.
INSERT INTO channel_bindings (agent_id, platform, account_id, peer, owner_peer)
SELECT b.agent_id, b.platform, b.account_id, @peer, b.owner_peer FROM channel_bindings b
WHERE b.agent_id = @agent_id AND b.platform = 'telegram' AND b.account_id = @account_id
  AND b.owner_peer = @owner_peer AND b.peer = b.owner_peer AND b.revoked_at IS NULL
ON CONFLICT DO NOTHING;

-- name: RevokeChannelBindings :exec
UPDATE channel_bindings SET revoked_at = unixepoch() * 1000
WHERE agent_id = @agent_id AND platform = @platform AND revoked_at IS NULL;

-- name: ChannelBindingActive :one
SELECT EXISTS (SELECT 1 FROM channel_bindings WHERE id = sqlc.narg(binding_id) AND revoked_at IS NULL);

-- name: CanSendCommandReply :one
SELECT EXISTS (
    SELECT 1 FROM channel_commands c JOIN channel_bindings b ON b.id = c.binding_id
    WHERE c.source_key = ? AND c.delivered_at IS NULL AND b.revoked_at IS NULL
);

-- name: CanSendOutbox :one
SELECT EXISTS (
    SELECT 1 FROM outbox_messages o JOIN channel_bindings b ON b.id = o.binding_id
    WHERE o.id = ? AND o.status = 'pending' AND b.revoked_at IS NULL
);
