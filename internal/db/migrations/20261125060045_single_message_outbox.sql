-- +goose Up
DROP TRIGGER bind_telegram_insert;
DROP TRIGGER bind_telegram_update;
DROP TRIGGER bind_telegram_delete;
DROP TRIGGER bind_feishu_insert;
DROP TRIGGER bind_feishu_update;
DROP TRIGGER bind_feishu_delete;
DROP TRIGGER bind_wechat_insert;
DROP TRIGGER bind_wechat_update;
DROP TRIGGER bind_wechat_delete;
DROP TRIGGER sync_channel_binding_addresses;
DROP TRIGGER revoke_binding_deliveries;
DROP TRIGGER seal_unbound_outbox;
DROP VIEW sync_channel_bindings;
DROP INDEX outbox_pending_binding;
DROP INDEX submissions_channel_binding;
ALTER TABLE submissions DROP COLUMN binding_id;
ALTER TABLE channel_commands DROP COLUMN binding_id;

DROP VIEW outbox_delivery_diagnostics;
DROP TRIGGER release_outbox_files;
DROP TRIGGER release_unanswered_outbox_files;
DROP TRIGGER revoke_outbox_on_channel_result_revocation;
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
          AND EXISTS (SELECT 1 FROM channel_bindings b WHERE b.id = o.binding_id AND b.revoked_at IS NULL)
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
DROP TABLE channel_bindings;

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

-- +goose Down
-- Delivery state cannot be converted back to a multipart cursor safely.
CREATE TEMP VIEW _single_message_outbox_no_downgrade AS SELECT 1 AS v;
-- +goose StatementBegin
CREATE TEMP TRIGGER _single_message_outbox_no_downgrade_trg INSTEAD OF INSERT ON _single_message_outbox_no_downgrade
BEGIN
    SELECT RAISE(ABORT, 'single-message outbox cannot be downgraded');
END;
-- +goose StatementEnd
INSERT INTO _single_message_outbox_no_downgrade VALUES (1);
