-- name: GetTelegramAlbumMember :one
SELECT * FROM telegram_album_members WHERE source_key = ?;

-- name: StoreTelegramAlbumMember :execrows
INSERT INTO telegram_album_members (source_key, agent_id, account_id, sender_id, peer, group_key, input_json, received_at)
SELECT @source_key, a.id, @account_id, @sender_id, @peer, @group_key, @input_json, @received_at
FROM agents a JOIN telegram_settings t ON t.agent_id = a.id
WHERE a.id = @agent_id AND a.archived_at IS NULL
  AND substr(t.token, 1, instr(t.token, ':') - 1) = @account_id
  AND instr(',' || replace(t.allowed_users, ' ', '') || ',', ',' || @sender_id || ',') > 0
ON CONFLICT DO NOTHING;

-- name: ListCollectingTelegramAlbums :many
SELECT group_key, CAST(min(received_at) AS INTEGER) AS first_at, CAST(max(received_at) AS INTEGER) AS last_at, count(*) AS members
FROM telegram_album_members WHERE agent_id = ? AND state = 'collecting'
GROUP BY group_key ORDER BY min(received_at), min(rowid);

-- name: ListUnsealedTelegramAlbumMembers :many
SELECT * FROM telegram_album_members
WHERE agent_id = ? AND group_key = ? AND state = 'collecting'
ORDER BY received_at, rowid LIMIT 10;

-- name: SealTelegramAlbumMembers :exec
UPDATE telegram_album_members SET batch_source = @batch_source, state = 'sealed'
WHERE agent_id = @agent_id AND state = 'collecting'
  AND source_key IN (SELECT value FROM json_each(@sources));

-- name: ListSealedTelegramAlbums :many
SELECT batch_source FROM telegram_album_members
WHERE agent_id = ? AND state = 'sealed'
GROUP BY batch_source ORDER BY min(received_at), min(rowid);

-- name: ListTelegramAlbumBatchMembers :many
SELECT * FROM telegram_album_members
WHERE agent_id = ? AND batch_source = ? AND state = 'sealed'
ORDER BY received_at, rowid;

-- name: HasEarlierTelegramAlbumBatch :one
SELECT EXISTS(SELECT 1 FROM telegram_album_members WHERE agent_id = @agent_id
 AND group_key = @group_key AND batch_source <> '' AND batch_source <> @batch_source);

-- name: TransferTelegramAlbumMembers :exec
UPDATE telegram_album_members SET state = 'transferred', input_json = '{}'
WHERE agent_id = ? AND batch_source = ? AND state = 'sealed';

-- name: ListDiscardedTelegramAlbumMembers :many
SELECT * FROM telegram_album_members
WHERE agent_id = ? AND state = 'discarded' AND input_json <> '{}';

-- name: ClearDiscardedTelegramAlbumMember :exec
UPDATE telegram_album_members SET input_json = '{}'
WHERE agent_id = ? AND source_key = ? AND state = 'discarded';

-- name: DiscardTelegramAlbums :exec
UPDATE telegram_album_members SET state = 'discarded'
WHERE agent_id = ? AND state IN ('collecting', 'sealed');

-- name: IsTelegramAlbumSealed :one
SELECT EXISTS(SELECT 1 FROM telegram_album_members
WHERE agent_id = @agent_id AND batch_source = @batch_source AND state = 'sealed');
