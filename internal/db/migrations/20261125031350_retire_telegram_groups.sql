-- +goose Up
-- Before group scopes, each group had a peer-keyed conversation in the agent store.
-- Keep its native ID only so startup can isolate it before the Harness resumes.
UPDATE conversations SET execution_scope = 'retired-telegram-legacy'
WHERE execution_scope = '' AND channel_key LIKE 'telegram:-%';

-- Keep the old routing key and native namespace as archive metadata. Native IDs
-- are local to each store, so clearing the namespace would alias private IDs.
UPDATE conversations
SET archived_at = coalesce(archived_at, unixepoch() * 1000), abort_pending = 0
WHERE execution_scope <> '';

UPDATE submissions
SET status = CASE WHEN status = 'pending' THEN 'unanswered' ELSE status END,
    reason = CASE WHEN status = 'pending' THEN 'Telegram group retired' ELSE reason END,
    settled_at = CASE WHEN status = 'pending' THEN unixepoch() * 1000 ELSE settled_at END,
    delivered_at = coalesce(delivered_at, unixepoch() * 1000),
    stop_requested = 0, channel_revoked = 1
WHERE origin LIKE 'telegram:-%'
   OR conversation_id IN (SELECT id FROM conversations WHERE execution_scope <> '');

UPDATE outbox_messages SET status = 'revoked', error = 'Telegram group retired'
WHERE status = 'pending' AND (
    peer LIKE 'telegram:-%'
    OR conversation_id IN (SELECT id FROM conversations WHERE execution_scope <> '')
    OR source_submission_id IN (
        SELECT id FROM submissions WHERE origin LIKE 'telegram:-%'
           OR conversation_id IN (SELECT id FROM conversations WHERE execution_scope <> '')
    )
);

UPDATE channel_commands SET completed = 1, delivered_at = unixepoch() * 1000
WHERE peer LIKE 'telegram:-%' AND delivered_at IS NULL;

UPDATE telegram_album_members SET state = 'discarded'
WHERE peer LIKE 'telegram:-%' AND state IN ('collecting', 'sealed');

UPDATE telegram_callbacks SET consumed_at = unixepoch() * 1000
WHERE (peer LIKE 'telegram:-%' OR execution_scope <> '') AND consumed_at IS NULL;

DELETE FROM main_turn_stops
WHERE conversation_id IN (SELECT id FROM conversations WHERE execution_scope <> '');

-- +goose Down
-- Do not replay retired work on downgrade.
SELECT 1;
