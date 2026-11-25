-- name: ChannelPaired :one
SELECT EXISTS (SELECT 1 FROM configured_channel_addresses
WHERE agent_id = @agent_id AND peer = @peer AND account_id = @account_id);

-- name: ListPairedChannel :many
SELECT CAST(peer AS TEXT) AS peer, account_id FROM configured_channel_addresses
WHERE agent_id = @agent_id AND platform = @platform ORDER BY peer;

-- name: CanSendOutbox :one
SELECT EXISTS (SELECT 1 FROM outbox_messages WHERE id = ? AND status = 'pending');
