-- +goose Up
CREATE TABLE channel_bindings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    platform TEXT NOT NULL CHECK (platform IN ('telegram', 'feishu', 'wechat')),
    account_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    owner_peer TEXT NOT NULL,
    revoked_at INTEGER
) STRICT;
CREATE UNIQUE INDEX channel_bindings_active ON channel_bindings (agent_id, platform, peer, owner_peer) WHERE revoked_at IS NULL;

ALTER TABLE submissions ADD COLUMN binding_id INTEGER REFERENCES channel_bindings(id);
ALTER TABLE outbox_messages ADD COLUMN binding_id INTEGER REFERENCES channel_bindings(id);
ALTER TABLE channel_commands ADD COLUMN binding_id INTEGER REFERENCES channel_bindings(id);
CREATE INDEX outbox_pending_binding ON outbox_messages (binding_id) WHERE status = 'pending';
CREATE INDEX submissions_channel_binding ON submissions (binding_id) WHERE binding_id IS NOT NULL;

-- No legacy delivery or input proves continuity with a current pairing.
UPDATE outbox_messages SET status = 'revoked', error = 'delivery predates channel binding'
WHERE status = 'pending' AND peer <> '';
UPDATE channel_commands SET completed = 1, delivered_at = unixepoch() * 1000,
    answer = CASE WHEN command = '/feedback' THEN '' ELSE answer END
WHERE delivered_at IS NULL;
DELETE FROM telegram_callbacks;
DELETE FROM outbox_files WHERE outbox_id IS NULL AND submission_id IN (
    SELECT id FROM submissions WHERE origin LIKE 'telegram:%'
);
UPDATE channel_webhook_inbox SET payload = NULL WHERE payload IS NOT NULL;

DROP TRIGGER revoke_deleted_telegram_outbox;
DROP TRIGGER revoke_deleted_telegram_replies;
DROP TRIGGER revoke_changed_telegram_replies;
DROP TRIGGER revoke_deleted_feishu_outbox;
DROP TRIGGER revoke_changed_feishu_outbox;
DROP TRIGGER revoke_deleted_wechat_replies;
DROP TRIGGER revoke_changed_wechat_replies;
DROP TRIGGER revoke_deleted_telegram_targets;
DROP TRIGGER revoke_changed_telegram_targets;
DROP TRIGGER revoke_deleted_feishu_targets;
DROP TRIGGER revoke_changed_feishu_targets;
DROP TRIGGER revoke_deleted_wechat_targets;
DROP TRIGGER revoke_changed_wechat_targets;

-- Project the configured addresses. A settings write reconciles only its own platform.
CREATE VIEW configured_channel_addresses AS
SELECT s.agent_id, 'telegram' AS platform, substr(s.token, 1, instr(s.token, ':') - 1) AS account_id,
       'telegram:' || CAST(u.value AS INTEGER) AS peer
FROM telegram_settings s, json_each('[' || replace(json_quote(replace(s.allowed_users, ' ', '')), ',', '","') || ']') u
WHERE CAST(u.value AS INTEGER) > 0 AND CAST(CAST(u.value AS INTEGER) AS TEXT) = u.value
UNION ALL
SELECT agent_id, 'feishu', app_id, 'feishu:' || owner_id FROM feishu_settings WHERE owner_id <> ''
UNION ALL
SELECT agent_id, 'wechat', bot_id, 'wechat:' || owner FROM wechat_settings WHERE owner <> '';

INSERT INTO channel_bindings (agent_id, platform, account_id, peer, owner_peer)
SELECT DISTINCT agent_id, platform, account_id, peer, peer FROM configured_channel_addresses;

CREATE VIEW sync_channel_bindings AS SELECT agent_id, platform FROM channel_bindings WHERE 0;

-- +goose StatementBegin
CREATE TRIGGER sync_channel_binding_addresses INSTEAD OF INSERT ON sync_channel_bindings
BEGIN
    UPDATE channel_bindings SET revoked_at = unixepoch() * 1000
    WHERE agent_id = NEW.agent_id AND platform = NEW.platform AND revoked_at IS NULL
      AND NOT EXISTS (SELECT 1 FROM configured_channel_addresses a
                      WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.platform
                        AND a.account_id = channel_bindings.account_id AND a.peer = channel_bindings.owner_peer);
    INSERT INTO channel_bindings (agent_id, platform, account_id, peer, owner_peer)
    SELECT DISTINCT a.agent_id, a.platform, a.account_id, a.peer, a.peer FROM configured_channel_addresses a
    WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.platform
      AND NOT EXISTS (SELECT 1 FROM channel_bindings b WHERE b.agent_id = a.agent_id AND b.platform = a.platform
                        AND b.peer = a.peer AND b.owner_peer = a.peer AND b.revoked_at IS NULL);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_binding_deliveries AFTER UPDATE OF revoked_at ON channel_bindings
WHEN OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL
BEGIN
    UPDATE outbox_messages SET status = 'revoked', error = 'channel binding no longer active'
    WHERE binding_id = NEW.id AND status = 'pending';
    UPDATE channel_commands SET delivered_at = unixepoch() * 1000, answer = ''
    WHERE binding_id = NEW.id AND delivered_at IS NULL;
    DELETE FROM outbox_files WHERE outbox_id IS NULL AND submission_id IN (
        SELECT id FROM submissions WHERE binding_id = NEW.id
    );
    DELETE FROM telegram_callbacks
    WHERE NEW.platform = 'telegram' AND agent_id = NEW.agent_id AND peer = NEW.peer AND consumed_at IS NULL;
    DELETE FROM routine_notification_targets
    WHERE agent_id = NEW.agent_id AND platform = NEW.platform AND account_id = NEW.account_id AND peer = NEW.peer;
    -- Raw callbacks lack a verified peer until the receiver normalizes them.
    UPDATE channel_webhook_inbox SET payload = NULL
    WHERE agent_id = NEW.agent_id AND platform = NEW.platform AND account = NEW.account_id AND payload IS NOT NULL;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER seal_unbound_outbox AFTER INSERT ON outbox_messages
WHEN NEW.status = 'pending' AND NEW.peer <> '' AND NOT EXISTS (
    SELECT 1 FROM channel_bindings b WHERE b.id = NEW.binding_id AND b.revoked_at IS NULL
      AND b.agent_id = NEW.agent_id AND b.peer = NEW.peer
)
BEGIN
    UPDATE outbox_messages SET status = 'revoked', error = 'channel binding no longer active' WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER bind_telegram_insert AFTER INSERT ON telegram_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (NEW.agent_id, 'telegram');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_telegram_update AFTER UPDATE OF token, allowed_users ON telegram_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (NEW.agent_id, 'telegram');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_telegram_delete AFTER DELETE ON telegram_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (OLD.agent_id, 'telegram');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_feishu_insert AFTER INSERT ON feishu_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (NEW.agent_id, 'feishu');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_feishu_update AFTER UPDATE OF app_id, owner_id ON feishu_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (NEW.agent_id, 'feishu');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_feishu_delete AFTER DELETE ON feishu_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (OLD.agent_id, 'feishu');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_wechat_insert AFTER INSERT ON wechat_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (NEW.agent_id, 'wechat');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_wechat_update AFTER UPDATE OF bot_id, owner ON wechat_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (NEW.agent_id, 'wechat');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER bind_wechat_delete AFTER DELETE ON wechat_settings BEGIN
    INSERT INTO sync_channel_bindings VALUES (OLD.agent_id, 'wechat');
END;
-- +goose StatementEnd

-- +goose Down
-- A server without binding support cannot safely resume these deliveries.
UPDATE outbox_messages SET status = 'revoked', error = 'channel binding removed by downgrade'
WHERE status = 'pending' AND peer <> '';
UPDATE submissions SET channel_revoked = 1 WHERE origin LIKE 'telegram:%' OR origin LIKE 'feishu:%' OR origin LIKE 'wechat:%';
UPDATE channel_commands SET completed = 1, delivered_at = unixepoch() * 1000 WHERE delivered_at IS NULL;
UPDATE channel_webhook_inbox SET payload = NULL WHERE payload IS NOT NULL;
DELETE FROM telegram_callbacks;

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
DROP VIEW configured_channel_addresses;
DROP INDEX outbox_pending_binding;
DROP INDEX submissions_channel_binding;
ALTER TABLE channel_commands DROP COLUMN binding_id;
ALTER TABLE outbox_messages DROP COLUMN binding_id;
ALTER TABLE submissions DROP COLUMN binding_id;
DROP TABLE channel_bindings;

-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_telegram_outbox BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE outbox_messages
    SET status = 'revoked', error = 'Telegram authorization revoked'
    WHERE status = 'pending' AND peer LIKE 'telegram:%' AND agent_id = OLD.agent_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_telegram_replies BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'Telegram authorization revoked'
    WHERE channel_revoked = 0 AND origin LIKE 'telegram:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = OLD.agent_id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_changed_telegram_replies AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'Telegram authorization revoked'
    WHERE channel_revoked = 0 AND origin LIKE 'telegram:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = NEW.agent_id)
      AND (
        coalesce(json_extract(channel_context, '$.accountId'), substr(OLD.token, 1, instr(OLD.token, ':') - 1))
          <> substr(NEW.token, 1, instr(NEW.token, ':') - 1)
        OR instr(',' || replace(NEW.allowed_users, ' ', '') || ',',
          ',' || coalesce(json_extract(channel_context, '$.senderId'), substr(origin, 10)) || ',') = 0
      );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_feishu_outbox AFTER DELETE ON feishu_settings
BEGIN
    UPDATE outbox_messages
    SET status = 'revoked', error = 'Feishu authorization revoked'
    WHERE agent_id = OLD.agent_id AND status = 'pending' AND peer LIKE 'feishu:%';
    UPDATE submissions
    SET channel_revoked = 1, channel_delivery_error = 'Feishu authorization revoked'
    WHERE channel_revoked = 0 AND origin LIKE 'feishu:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = OLD.agent_id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_changed_feishu_outbox AFTER UPDATE OF app_id, owner_id ON feishu_settings
WHEN OLD.app_id <> NEW.app_id OR OLD.owner_id <> NEW.owner_id
BEGIN
    UPDATE outbox_messages
    SET status = 'revoked', error = 'Feishu authorization revoked'
    WHERE agent_id = NEW.agent_id AND status = 'pending' AND peer LIKE 'feishu:%'
      AND (OLD.app_id <> NEW.app_id OR NEW.owner_id = '' OR peer <> 'feishu:' || NEW.owner_id);
    UPDATE submissions
    SET channel_revoked = 1, channel_delivery_error = 'Feishu authorization revoked'
    WHERE channel_revoked = 0 AND origin LIKE 'feishu:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = NEW.agent_id)
      AND (OLD.app_id <> NEW.app_id OR NEW.owner_id = '' OR origin <> 'feishu:' || NEW.owner_id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_wechat_replies AFTER DELETE ON wechat_settings
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'WeChat authorization revoked'
    WHERE channel_revoked = 0 AND origin LIKE 'wechat:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = OLD.agent_id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_changed_wechat_replies AFTER UPDATE OF bot_id, owner ON wechat_settings
WHEN OLD.bot_id <> NEW.bot_id OR OLD.owner <> NEW.owner
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'WeChat authorization revoked'
    WHERE channel_revoked = 0 AND origin LIKE 'wechat:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = NEW.agent_id)
      AND (OLD.bot_id <> NEW.bot_id OR NEW.owner = '' OR origin <> 'wechat:' || NEW.owner);
END;
-- +goose StatementEnd

CREATE TRIGGER revoke_deleted_telegram_targets AFTER DELETE ON telegram_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = OLD.agent_id AND platform = 'telegram'; END;
-- +goose StatementBegin
CREATE TRIGGER revoke_changed_telegram_targets AFTER UPDATE ON telegram_settings
BEGIN DELETE FROM routine_notification_targets
    WHERE agent_id = NEW.agent_id AND platform = 'telegram'
      AND (account_id <> substr(NEW.token, 1, instr(NEW.token, ':') - 1)
        OR instr(',' || replace(NEW.allowed_users, ' ', '') || ',', ',' || substr(peer, 10) || ',') = 0);
END;
-- +goose StatementEnd
CREATE TRIGGER revoke_deleted_feishu_targets AFTER DELETE ON feishu_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = OLD.agent_id AND platform = 'feishu'; END;
-- +goose StatementBegin
CREATE TRIGGER revoke_changed_feishu_targets AFTER UPDATE ON feishu_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = NEW.agent_id AND platform = 'feishu'
  AND (account_id <> NEW.app_id OR peer <> 'feishu:' || NEW.owner_id OR NEW.owner_id = ''); END;
-- +goose StatementEnd
CREATE TRIGGER revoke_deleted_wechat_targets AFTER DELETE ON wechat_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = OLD.agent_id AND platform = 'wechat'; END;
-- +goose StatementBegin
CREATE TRIGGER revoke_changed_wechat_targets AFTER UPDATE ON wechat_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = NEW.agent_id AND platform = 'wechat'
  AND (account_id <> NEW.bot_id OR peer <> 'wechat:' || NEW.owner OR NEW.owner = ''); END;
-- +goose StatementEnd
