-- +goose Up
-- Consolidated single-owner architecture migration (post v0.20.0-rc.6):
-- 1. Retire Telegram group chats and seal their pending work.
-- 2. Replace legacy per-platform revocation triggers with configured_channel_addresses pairing checks.
-- 3. Rebuild outbox_messages as a strict single-message ledger and split out web_notifications.
-- 4. Unify Inbound Inbox routing metadata (producer, reply_channel, reply_peer) on submissions.
-- 5. Drop the retired channel_follow_ups state machine.

-- 1. Retire Telegram group chats.
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

UPDATE channel_commands SET completed = 1, delivered_at = unixepoch() * 1000,
    answer = CASE WHEN command = '/feedback' THEN '' ELSE answer END
WHERE delivered_at IS NULL;

UPDATE telegram_album_members SET state = 'discarded'
WHERE peer LIKE 'telegram:-%' AND state IN ('collecting', 'sealed');

DELETE FROM telegram_callbacks WHERE consumed_at IS NULL;

DELETE FROM main_turn_stops
WHERE conversation_id IN (SELECT id FROM conversations WHERE execution_scope <> '');

-- 2. Replace legacy revocation triggers with configured_channel_addresses.
DROP TRIGGER IF EXISTS revoke_deleted_telegram_outbox;
DROP TRIGGER IF EXISTS revoke_deleted_telegram_replies;
DROP TRIGGER IF EXISTS revoke_changed_telegram_replies;
DROP TRIGGER IF EXISTS revoke_deleted_feishu_outbox;
DROP TRIGGER IF EXISTS revoke_changed_feishu_outbox;
DROP TRIGGER IF EXISTS revoke_deleted_wechat_replies;
DROP TRIGGER IF EXISTS revoke_changed_wechat_replies;
DROP TRIGGER IF EXISTS revoke_deleted_telegram_targets;
DROP TRIGGER IF EXISTS revoke_changed_telegram_targets;
DROP TRIGGER IF EXISTS revoke_deleted_feishu_targets;
DROP TRIGGER IF EXISTS revoke_changed_feishu_targets;
DROP TRIGGER IF EXISTS revoke_deleted_wechat_targets;
DROP TRIGGER IF EXISTS revoke_changed_wechat_targets;

CREATE VIEW configured_channel_addresses AS
SELECT s.agent_id, 'telegram' AS platform, substr(s.token, 1, instr(s.token, ':') - 1) AS account_id,
       'telegram:' || CAST(u.value AS INTEGER) AS peer
FROM telegram_settings s, json_each('[' || replace(json_quote(replace(s.allowed_users, ' ', '')), ',', '","') || ']') u
WHERE CAST(u.value AS INTEGER) > 0 AND CAST(CAST(u.value AS INTEGER) AS TEXT) = u.value
UNION ALL
SELECT agent_id, 'feishu', app_id, 'feishu:' || owner_id FROM feishu_settings WHERE owner_id <> ''
UNION ALL
SELECT agent_id, 'wechat', bot_id, 'wechat:' || owner FROM wechat_settings WHERE owner <> '';

-- 3. Single-message Outbox + web_notifications.
DROP VIEW IF EXISTS outbox_delivery_diagnostics;
DROP TRIGGER IF EXISTS release_outbox_files;
DROP TRIGGER IF EXISTS release_unanswered_outbox_files;
DROP TRIGGER IF EXISTS revoke_outbox_on_channel_result_revocation;
ALTER TABLE outbox_files RENAME TO legacy_outbox_files;
ALTER TABLE outbox_messages RENAME TO legacy_outbox_messages;

CREATE TABLE outbox_messages (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_submission_id TEXT REFERENCES submissions (id),
    kind TEXT NOT NULL CHECK (kind IN ('reply', 'proactive', 'question', 'routine', 'system_error', 'file', 'control', 'feedback')),
    peer TEXT NOT NULL CHECK (instr(peer, ':') > 1 AND instr(peer, ':') < length(peer)),
    channel TEXT GENERATED ALWAYS AS (CASE WHEN instr(peer, ':') > 0 THEN substr(peer, 1, instr(peer, ':') - 1) ELSE '' END) STORED,
    target TEXT GENERATED ALWAYS AS (CASE WHEN instr(peer, ':') > 0 THEN substr(peer, instr(peer, ':') + 1) ELSE '' END) STORED,
    channel_context TEXT NOT NULL DEFAULT '{}',
    topic_key TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    rendered_message TEXT NOT NULL DEFAULT '',
    remote_ids TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'superseded', 'failed')),
    retry_at INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    delivered_at INTEGER,
    UNIQUE (event_id, channel, target)
) STRICT;

CREATE TABLE web_notifications (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_submission_id TEXT NOT NULL REFERENCES submissions (id),
    body TEXT NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX web_notifications_conversation ON web_notifications (conversation_id);
INSERT INTO web_notifications (rowid, id, event_id, agent_id, conversation_id, source_submission_id, body, created_at)
SELECT o.rowid, o.id, 'notify:' || substr(o.dedup_key, 6), o.agent_id, o.conversation_id, o.source_submission_id, o.body, o.created_at
FROM legacy_outbox_messages o JOIN submissions s ON s.id = o.source_submission_id
WHERE o.kind = 'proactive' AND o.dedup_key LIKE 'tool:%' AND s.origin = 'web';

-- Never replay a legacy group or an already attempted multipart message. Its
-- confirmed platform messages remain in the diagnostic record. A frozen single
-- message keeps the same payload and delivery ID on recovery.
INSERT INTO outbox_messages (
    rowid, id, event_id, agent_id, conversation_id, source_submission_id, kind,
    peer, channel_context, topic_key, body, reason, rendered_message, remote_ids,
    status, retry_at, error, created_at, delivered_at
)
SELECT rowid, id, CASE WHEN dedup_key LIKE 'tool:%' THEN 'notify:' || substr(dedup_key, 6) ELSE dedup_key END,
    agent_id, conversation_id, source_submission_id, kind,
    peer, channel_context, topic_key, body, reason,
    CASE WHEN json_array_length(nullif(rendered_parts, '')) = 1 THEN rendered_parts -> '$[0]' ELSE '' END,
    remote_ids,
    CASE
        WHEN status = 'delivered' AND skipped_part = 0 THEN 'sent'
        WHEN status = 'pending' AND delivered_parts = 1 AND group_cursor = 0
          AND json_array_length(nullif(rendered_parts, '')) = 1 AND skipped_part = 0
          AND json_array_length(remote_ids) = 1 THEN 'sent'
        WHEN status = 'pending' AND peer <> '' AND delivered_parts = 0 AND group_cursor = 0 AND group_fallback = 0
          AND skipped_part = 0 AND (rendered_parts = '' OR (json_array_length(rendered_parts) = 1 AND
            json_type(rendered_parts, '$[0].group') IS NULL))
          AND (kind NOT IN ('reply', 'question') OR EXISTS (
              SELECT 1 FROM submissions s WHERE s.id = o.source_submission_id AND s.kind = 'message'
          ))
          AND kind <> 'system_error'
          AND NOT EXISTS (SELECT 1 FROM legacy_outbox_files f WHERE f.outbox_id = o.id)
          AND EXISTS (
              SELECT 1 FROM configured_channel_addresses a
              WHERE a.agent_id = o.agent_id
                AND a.platform = substr(o.peer, 1, instr(o.peer, ':') - 1)
                AND a.account_id = o.channel_context ->> 'accountId'
                AND a.peer = CASE WHEN substr(o.peer, 1, instr(o.peer, ':') - 1) = 'telegram'
                    THEN coalesce('telegram:' || nullif(o.channel_context ->> 'senderId', ''), o.peer)
                    ELSE o.peer END
          )
          THEN 'pending'
        WHEN status = 'superseded' THEN 'superseded'
        ELSE 'failed'
    END,
    retry_at,
    error, created_at, delivered_at
FROM legacy_outbox_messages o
WHERE instr(peer, ':') > 1 AND instr(peer, ':') < length(peer);
UPDATE outbox_messages SET error = 'legacy delivery did not complete before the single-message upgrade'
WHERE status = 'failed' AND error = '';
UPDATE outbox_messages SET delivered_at = unixepoch() * 1000, error = '' WHERE status = 'sent' AND delivered_at IS NULL;

CREATE TABLE outbox_files (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    operation_key TEXT NOT NULL,
    submission_id TEXT REFERENCES submissions (id) ON DELETE CASCADE,
    outbox_id TEXT REFERENCES outbox_messages (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    body BLOB NOT NULL CHECK (length(body) <= 20971520),
    UNIQUE (agent_id, operation_key)
) STRICT;
INSERT INTO outbox_files SELECT * FROM legacy_outbox_files WHERE outbox_id IS NULL;
DROP TABLE legacy_outbox_files;
DROP TABLE legacy_outbox_messages;

UPDATE telegram_settings SET options = json_remove(options, '$.preview');

CREATE INDEX outbox_messages_pending ON outbox_messages (agent_id, channel, peer) WHERE status = 'pending';
CREATE INDEX outbox_messages_source ON outbox_messages (source_submission_id) WHERE source_submission_id IS NOT NULL;
CREATE INDEX outbox_messages_conversation ON outbox_messages (conversation_id, created_at DESC);
CREATE INDEX outbox_files_submission ON outbox_files (submission_id) WHERE submission_id IS NOT NULL;
CREATE UNIQUE INDEX outbox_files_outbox ON outbox_files (outbox_id) WHERE outbox_id IS NOT NULL;

-- +goose StatementBegin
CREATE TRIGGER release_outbox_files AFTER UPDATE OF status ON outbox_messages
WHEN NEW.status != 'pending'
BEGIN DELETE FROM outbox_files WHERE outbox_id = NEW.id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER release_unanswered_outbox_files AFTER UPDATE OF status ON submissions
WHEN NEW.status = 'unanswered'
BEGIN DELETE FROM outbox_files WHERE submission_id = NEW.id AND outbox_id IS NULL; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_outbox_on_channel_result_revocation AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.kind = 'task_result' AND OLD.channel_revoked = 0 AND NEW.channel_revoked <> 0
BEGIN
    UPDATE outbox_messages SET status = 'failed', error = NEW.channel_delivery_error, retry_at = 0
    WHERE source_submission_id = NEW.id AND status = 'pending';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER finish_outbox_command AFTER UPDATE OF status ON outbox_messages
WHEN NEW.kind IN ('control', 'feedback') AND NEW.status IN ('sent', 'failed')
BEGIN
    UPDATE channel_commands SET delivered_at = coalesce(NEW.delivered_at, unixepoch() * 1000),
        answer = CASE WHEN NEW.kind = 'feedback' THEN '' ELSE answer END,
        rendered_reply = CASE WHEN NEW.kind = 'feedback' THEN '' ELSE rendered_reply END
    WHERE source_key = NEW.id;
    UPDATE outbox_messages SET rendered_message = '' WHERE id = NEW.id AND NEW.kind = 'feedback';
END;
-- +goose StatementEnd

CREATE VIEW outbox_delivery_diagnostics AS
SELECT o.id, o.source_submission_id, o.peer, o.remote_ids,
    CASE
        WHEN o.peer <> '' AND EXISTS (
            SELECT 1 FROM json_each(o.remote_ids) r WHERE coalesce(r.value ->> 'id', '') <> ''
        ) THEN 'platform_confirmed'
        WHEN o.status = 'failed' THEN 'revoked_or_skipped_unconfirmed'
        ELSE 'unconfirmed'
    END AS disposition
FROM outbox_messages o;

-- Telegram's private chat ID and sender ID can differ. Only the sender is on
-- the whitelist. The stored account prevents delivery through a different bot.
-- +goose StatementBegin
CREATE TRIGGER fail_unpaired_outbox AFTER INSERT ON outbox_messages
WHEN NEW.status = 'pending' AND NEW.peer <> '' AND NOT EXISTS (
    SELECT 1 FROM configured_channel_addresses a
    WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.channel
      AND a.account_id = NEW.channel_context ->> 'accountId'
      AND a.peer = CASE WHEN NEW.channel = 'telegram'
          THEN coalesce('telegram:' || nullif(NEW.channel_context ->> 'senderId', ''), NEW.peer)
          ELSE NEW.peer END
)
BEGIN
    UPDATE outbox_messages SET status = 'failed', error = 'channel is not paired' WHERE id = NEW.id;
END;
-- +goose StatementEnd

CREATE VIEW cancel_unpaired_channel_work AS SELECT agent_id, platform FROM configured_channel_addresses WHERE 0;
-- +goose StatementBegin
CREATE TRIGGER cancel_unpaired_channel_work INSTEAD OF INSERT ON cancel_unpaired_channel_work
BEGIN
    UPDATE outbox_messages SET status = 'failed', error = 'channel is not paired', retry_at = 0
    WHERE agent_id = NEW.agent_id AND channel = NEW.platform AND status = 'pending' AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.platform
          AND a.account_id = outbox_messages.channel_context ->> 'accountId'
          AND a.peer = CASE WHEN NEW.platform = 'telegram'
              THEN coalesce('telegram:' || nullif(outbox_messages.channel_context ->> 'senderId', ''), outbox_messages.peer)
              ELSE outbox_messages.peer END
    );
    UPDATE channel_commands SET delivered_at = unixepoch() * 1000, answer = '', rendered_reply = ''
    WHERE agent_id = NEW.agent_id AND peer LIKE NEW.platform || ':%' AND delivered_at IS NULL AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.platform
          AND a.account_id = channel_commands.channel_context ->> 'accountId'
          AND a.peer = CASE WHEN NEW.platform = 'telegram'
              THEN coalesce('telegram:' || nullif(channel_commands.channel_context ->> 'senderId', ''), channel_commands.peer)
              ELSE channel_commands.peer END
    );
    DELETE FROM telegram_callbacks
    WHERE NEW.platform = 'telegram' AND agent_id = NEW.agent_id AND consumed_at IS NULL AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.agent_id = NEW.agent_id AND a.platform = 'telegram'
          AND a.account_id = telegram_callbacks.channel_context ->> 'accountId'
          AND a.peer = 'telegram:' || (telegram_callbacks.channel_context ->> 'senderId')
    );
    DELETE FROM routine_notification_targets
    WHERE agent_id = NEW.agent_id AND platform = NEW.platform AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.platform
          AND a.account_id = routine_notification_targets.account_id AND a.peer = routine_notification_targets.peer
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_outbox_on_update AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (NEW.agent_id, 'telegram'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_outbox_on_delete AFTER DELETE ON telegram_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (OLD.agent_id, 'telegram'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_outbox_on_update AFTER UPDATE OF app_id, owner_id ON feishu_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (NEW.agent_id, 'feishu'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_outbox_on_delete AFTER DELETE ON feishu_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (OLD.agent_id, 'feishu'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_outbox_on_update AFTER UPDATE OF bot_id, owner ON wechat_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (NEW.agent_id, 'wechat'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_outbox_on_delete AFTER DELETE ON wechat_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (OLD.agent_id, 'wechat'); END;
-- +goose StatementEnd

-- 4. Unify Inbound Inbox contract on submissions.
ALTER TABLE submissions ADD COLUMN producer TEXT NOT NULL DEFAULT 'user' CHECK (producer IN ('user', 'sub_session', 'scheduler', 'plugin'));
ALTER TABLE submissions ADD COLUMN reply_channel TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN reply_peer TEXT NOT NULL DEFAULT '';

UPDATE submissions SET producer = CASE
    WHEN kind = 'message' AND origin = 'task' THEN 'sub_session'
    WHEN kind = 'message' AND origin IN ('automation', 'dream') THEN 'scheduler'
    WHEN kind = 'task_result' AND (
        result_child_id IS NOT NULL
        OR id LIKE 'result:%'
        OR id LIKE 'report:%'
        OR id LIKE 'err:%'
        OR id LIKE 'task:%:result'
    ) THEN 'sub_session'
    WHEN kind = 'task_result' AND (
        id LIKE 'monitor:%'
        OR id LIKE 'timer:%'
        OR id LIKE 'background:%'
        OR id LIKE 'routine:%'
    ) THEN 'scheduler'
    WHEN kind = 'task_result' AND id LIKE 'task-alert:%' THEN 'plugin'
    WHEN kind = 'task_result' THEN 'plugin'
    ELSE 'user'
END;

UPDATE submissions
SET reply_channel = substr(origin, 1, instr(origin, ':') - 1),
    reply_peer = substr(origin, instr(origin, ':') + 1)
WHERE producer = 'user'
    AND instr(origin, ':') > 1
    AND instr(origin, ':') < length(origin);

-- 5. Drop retired channel_follow_ups state machine.
DROP TABLE IF EXISTS channel_follow_ups;

-- +goose Down
-- Single-message outbox and retired group state cannot be safely downgraded.
CREATE TEMP VIEW _simplified_arch_no_downgrade AS SELECT 1 AS v;
-- +goose StatementBegin
CREATE TEMP TRIGGER _simplified_arch_no_downgrade_trg INSTEAD OF INSERT ON _simplified_arch_no_downgrade
BEGIN
    SELECT RAISE(ABORT, 'simplified single-owner architecture migration cannot be downgraded');
END;
-- +goose StatementEnd
INSERT INTO _simplified_arch_no_downgrade VALUES (1);
