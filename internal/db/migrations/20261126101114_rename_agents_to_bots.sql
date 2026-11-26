-- +goose Up
-- Drop dependent views and triggers before renaming their columns. Keep trigger order.
DROP TRIGGER fail_unpaired_outbox;
DROP TRIGGER cancel_unpaired_channel_work;
DROP TRIGGER clear_revoked_automation_target;
DROP TRIGGER automation_notification_target_update;
DROP TRIGGER automation_notification_target_insert;
DROP TRIGGER cancel_wechat_outbox_on_delete;
DROP TRIGGER cancel_wechat_outbox_on_update;
DROP TRIGGER cancel_feishu_outbox_on_delete;
DROP TRIGGER cancel_feishu_outbox_on_update;
DROP TRIGGER cancel_telegram_outbox_on_delete;
DROP TRIGGER cancel_telegram_outbox_on_update;
DROP TRIGGER telegram_managed_settings_delete;
DROP TRIGGER telegram_managed_settings_update;
DROP TRIGGER telegram_managed_settings_insert;
DROP TRIGGER cancel_archived_agent_monitors;
DROP TRIGGER discard_archived_telegram_effects;
DROP TRIGGER discard_revoked_telegram_effects;
DROP TRIGGER discard_deleted_telegram_effects;
DROP TRIGGER discard_archived_telegram_albums;
DROP TRIGGER revoke_changed_telegram_albums;
DROP TRIGGER revoke_deleted_telegram_albums;
DROP TRIGGER agent_notification_target_update;
DROP VIEW cancel_unpaired_channel_work;
DROP VIEW configured_channel_addresses;
DROP INDEX sessions_native;
DROP INDEX outbox_messages_pending;
DROP INDEX channel_webhook_pending;
DROP INDEX telegram_managed_setups_agent;
DROP INDEX telegram_effects_source;
DROP INDEX telegram_album_collect;
DROP INDEX channel_command_delivery;

-- Distinguish platform bot IDs from Dex Bot IDs.
ALTER TABLE wechat_settings RENAME COLUMN bot_id TO wechat_bot_id;
ALTER TABLE telegram_managed_setups RENAME COLUMN bot_id TO telegram_bot_id;
ALTER TABLE agents RENAME TO bots;
ALTER TABLE sessions RENAME COLUMN agent_id TO bot_id;
ALTER TABLE dreams RENAME COLUMN agent_id TO bot_id;
ALTER TABLE automations RENAME COLUMN agent_id TO bot_id;
ALTER TABLE telegram_settings RENAME COLUMN agent_id TO bot_id;
ALTER TABLE wechat_settings RENAME COLUMN agent_id TO bot_id;
ALTER TABLE feishu_settings RENAME COLUMN agent_id TO bot_id;
ALTER TABLE channel_commands RENAME COLUMN agent_id TO bot_id;
ALTER TABLE automation_tool_operations RENAME COLUMN agent_id TO bot_id;
ALTER TABLE automation_notification_targets RENAME COLUMN agent_id TO bot_id;
ALTER TABLE questions RENAME COLUMN agent_id TO bot_id;
ALTER TABLE questions RENAME COLUMN action_agent_id TO action_bot_id;
ALTER TABLE question_messages RENAME COLUMN agent_id TO bot_id;
ALTER TABLE telegram_album_members RENAME COLUMN agent_id TO bot_id;
ALTER TABLE telegram_effects RENAME COLUMN agent_id TO bot_id;
ALTER TABLE monitors RENAME COLUMN agent_id TO bot_id;
ALTER TABLE task_tool_operations RENAME COLUMN agent_id TO bot_id;
ALTER TABLE mcp_servers RENAME COLUMN agent_id TO bot_id;
ALTER TABLE telegram_managed_setups RENAME COLUMN agent_id TO bot_id;
ALTER TABLE channel_webhook_queue RENAME COLUMN agent_id TO bot_id;
ALTER TABLE web_notifications RENAME COLUMN agent_id TO bot_id;
ALTER TABLE telegram_callbacks RENAME COLUMN agent_id TO bot_id;
ALTER TABLE outbox_messages RENAME COLUMN agent_id TO bot_id;
ALTER TABLE outbox_files RENAME COLUMN agent_id TO bot_id;

CREATE INDEX channel_command_delivery ON channel_commands (bot_id, completed, delivered_at);
CREATE INDEX telegram_album_collect ON telegram_album_members(bot_id, state, group_key, batch_source);
CREATE INDEX telegram_effects_source ON telegram_effects(bot_id, source_key);
CREATE INDEX telegram_managed_setups_bot ON telegram_managed_setups(bot_id, expires_at);
CREATE INDEX channel_webhook_pending ON "channel_webhook_queue"(bot_id, platform, account, id) WHERE payload IS NOT NULL;
CREATE INDEX outbox_messages_pending ON outbox_messages (bot_id, channel, peer) WHERE status = 'pending';
CREATE UNIQUE INDEX sessions_native ON sessions (bot_id, execution_id) WHERE execution_id IS NOT NULL;

CREATE VIEW configured_channel_addresses AS
SELECT s.bot_id, 'telegram' AS platform, substr(s.token, 1, instr(s.token, ':') - 1) AS account_id,
       'telegram:' || CAST(u.value AS INTEGER) AS peer
FROM telegram_settings s, json_each('[' || replace(json_quote(replace(s.allowed_users, ' ', '')), ',', '","') || ']') u
WHERE CAST(u.value AS INTEGER) > 0 AND CAST(CAST(u.value AS INTEGER) AS TEXT) = u.value
UNION ALL
SELECT bot_id, 'feishu', app_id, 'feishu:' || owner_id FROM feishu_settings WHERE owner_id <> ''
UNION ALL
SELECT bot_id, 'wechat', wechat_bot_id, 'wechat:' || owner FROM wechat_settings WHERE owner <> '';
CREATE VIEW cancel_unpaired_channel_work AS SELECT bot_id, platform FROM configured_channel_addresses WHERE 0;

-- +goose StatementBegin
CREATE TRIGGER bot_notification_target_update BEFORE UPDATE OF primary_notification_target_id ON bots
WHEN NEW.primary_notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM "automation_notification_targets" t WHERE t.id = NEW.primary_notification_target_id AND t.bot_id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_telegram_albums BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE telegram_album_members SET state = 'discarded'
    WHERE bot_id = OLD.bot_id AND state IN ('collecting', 'sealed');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER revoke_changed_telegram_albums AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE telegram_album_members SET state = 'discarded'
    WHERE bot_id = NEW.bot_id AND state IN ('collecting', 'sealed')
      AND (account_id <> substr(NEW.token, 1, instr(NEW.token, ':') - 1)
        OR instr(',' || replace(NEW.allowed_users, ' ', '') || ',', ',' || sender_id || ',') = 0);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_archived_telegram_albums AFTER UPDATE OF archived_at ON bots
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE telegram_album_members SET state = 'discarded'
    WHERE bot_id = NEW.id AND state IN ('collecting', 'sealed');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_deleted_telegram_effects BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE telegram_effects SET state='discarded', body=NULL
    WHERE bot_id=OLD.bot_id AND (state IN ('ready','processing','unknown') OR input_json<>'{}' OR body IS NOT NULL);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_revoked_telegram_effects AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE telegram_effects SET state='discarded', body=NULL
    WHERE bot_id=NEW.bot_id AND (state IN ('ready','processing','unknown') OR input_json<>'{}' OR body IS NOT NULL)
      AND (account_id<>substr(NEW.token,1,instr(NEW.token,':')-1)
        OR instr(','||replace(NEW.allowed_users,' ','')||',',','||sender_id||',')=0);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_archived_telegram_effects AFTER UPDATE OF archived_at ON bots
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE telegram_effects SET state='discarded', body=NULL
    WHERE bot_id=NEW.id AND (state IN ('ready','processing','unknown') OR input_json<>'{}' OR body IS NOT NULL);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_archived_bot_monitors AFTER UPDATE OF archived_at ON bots
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE monitors
    SET state = 'cancelled', outcome = 'agent archived', running = 0, updated_at = NEW.archived_at
    WHERE bot_id = NEW.id AND state = 'scheduled';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER telegram_managed_settings_insert AFTER INSERT ON telegram_settings BEGIN
    UPDATE telegram_managed_setups SET state = 'canceled' WHERE bot_id = NEW.bot_id AND state = 'pending';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER telegram_managed_settings_update AFTER UPDATE OF token, allowed_users, options ON telegram_settings BEGIN
    UPDATE telegram_managed_setups SET state = 'canceled' WHERE bot_id = NEW.bot_id AND state = 'pending';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER telegram_managed_settings_delete AFTER DELETE ON telegram_settings BEGIN
    UPDATE telegram_managed_setups SET state = 'canceled' WHERE bot_id = OLD.bot_id AND state = 'pending';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_outbox_on_update AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (NEW.bot_id, 'telegram'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_outbox_on_delete AFTER DELETE ON telegram_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (OLD.bot_id, 'telegram'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_outbox_on_update AFTER UPDATE OF app_id, owner_id ON feishu_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (NEW.bot_id, 'feishu'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_outbox_on_delete AFTER DELETE ON feishu_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (OLD.bot_id, 'feishu'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_outbox_on_update AFTER UPDATE OF wechat_bot_id, owner ON wechat_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (NEW.bot_id, 'wechat'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_outbox_on_delete AFTER DELETE ON wechat_settings
BEGIN INSERT INTO cancel_unpaired_channel_work VALUES (OLD.bot_id, 'wechat'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER automation_notification_target_insert BEFORE INSERT ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM automation_notification_targets t WHERE t.id = NEW.notification_target_id AND t.bot_id = NEW.bot_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER automation_notification_target_update BEFORE UPDATE OF notification_target_id, bot_id ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM automation_notification_targets t WHERE t.id = NEW.notification_target_id AND t.bot_id = NEW.bot_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER clear_revoked_automation_target BEFORE DELETE ON automation_notification_targets
BEGIN
    UPDATE automations SET notification_route = 'none', notification_target_id = NULL
    WHERE bot_id = OLD.bot_id AND notification_target_id = OLD.id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_unpaired_channel_work INSTEAD OF INSERT ON cancel_unpaired_channel_work
BEGIN
    UPDATE outbox_messages SET status = 'failed', error = 'channel is not paired', retry_at = 0
    WHERE bot_id = NEW.bot_id AND channel = NEW.platform AND status = 'pending' AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.bot_id = NEW.bot_id AND a.platform = NEW.platform
          AND a.account_id = outbox_messages.channel_context ->> 'accountId'
          AND a.peer = CASE WHEN NEW.platform = 'telegram'
              THEN coalesce('telegram:' || nullif(outbox_messages.channel_context ->> 'senderId', ''), outbox_messages.peer)
              ELSE outbox_messages.peer END
    );
    UPDATE channel_commands SET delivered_at = unixepoch() * 1000, answer = ''
    WHERE bot_id = NEW.bot_id AND peer LIKE NEW.platform || ':%' AND delivered_at IS NULL AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.bot_id = NEW.bot_id AND a.platform = NEW.platform
          AND a.account_id = channel_commands.channel_context ->> 'accountId'
          AND a.peer = CASE WHEN NEW.platform = 'telegram'
              THEN coalesce('telegram:' || nullif(channel_commands.channel_context ->> 'senderId', ''), channel_commands.peer)
              ELSE channel_commands.peer END
    );
    DELETE FROM telegram_callbacks
    WHERE NEW.platform = 'telegram' AND bot_id = NEW.bot_id AND consumed_at IS NULL AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.bot_id = NEW.bot_id AND a.platform = 'telegram'
          AND a.account_id = telegram_callbacks.channel_context ->> 'accountId'
          AND a.peer = 'telegram:' || (telegram_callbacks.channel_context ->> 'senderId')
    );
    DELETE FROM automation_notification_targets
    WHERE bot_id = NEW.bot_id AND platform = NEW.platform AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.bot_id = NEW.bot_id AND a.platform = NEW.platform
          AND a.account_id = automation_notification_targets.account_id AND a.peer = automation_notification_targets.peer
    );
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER fail_unpaired_outbox AFTER INSERT ON outbox_messages
WHEN NEW.status = 'pending' AND NEW.peer <> '' AND NOT EXISTS (
    SELECT 1 FROM configured_channel_addresses a
    WHERE a.bot_id = NEW.bot_id AND a.platform = NEW.channel
      AND a.account_id = NEW.channel_context ->> 'accountId'
      AND a.peer = CASE WHEN NEW.channel = 'telegram'
          THEN coalesce('telegram:' || nullif(NEW.channel_context ->> 'senderId', ''), NEW.peer)
          ELSE NEW.peer END
)
BEGIN
    UPDATE outbox_messages SET status = 'failed', error = 'channel is not paired' WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER fail_unpaired_outbox;
DROP TRIGGER cancel_unpaired_channel_work;
DROP TRIGGER clear_revoked_automation_target;
DROP TRIGGER automation_notification_target_update;
DROP TRIGGER automation_notification_target_insert;
DROP TRIGGER cancel_wechat_outbox_on_delete;
DROP TRIGGER cancel_wechat_outbox_on_update;
DROP TRIGGER cancel_feishu_outbox_on_delete;
DROP TRIGGER cancel_feishu_outbox_on_update;
DROP TRIGGER cancel_telegram_outbox_on_delete;
DROP TRIGGER cancel_telegram_outbox_on_update;
DROP TRIGGER telegram_managed_settings_delete;
DROP TRIGGER telegram_managed_settings_update;
DROP TRIGGER telegram_managed_settings_insert;
DROP TRIGGER cancel_archived_bot_monitors;
DROP TRIGGER discard_archived_telegram_effects;
DROP TRIGGER discard_revoked_telegram_effects;
DROP TRIGGER discard_deleted_telegram_effects;
DROP TRIGGER discard_archived_telegram_albums;
DROP TRIGGER revoke_changed_telegram_albums;
DROP TRIGGER revoke_deleted_telegram_albums;
DROP TRIGGER bot_notification_target_update;
DROP VIEW cancel_unpaired_channel_work;
DROP VIEW configured_channel_addresses;
DROP INDEX sessions_native;
DROP INDEX outbox_messages_pending;
DROP INDEX channel_webhook_pending;
DROP INDEX telegram_managed_setups_bot;
DROP INDEX telegram_effects_source;
DROP INDEX telegram_album_collect;
DROP INDEX channel_command_delivery;

ALTER TABLE bots RENAME TO agents;
ALTER TABLE sessions RENAME COLUMN bot_id TO agent_id;
ALTER TABLE dreams RENAME COLUMN bot_id TO agent_id;
ALTER TABLE automations RENAME COLUMN bot_id TO agent_id;
ALTER TABLE telegram_settings RENAME COLUMN bot_id TO agent_id;
ALTER TABLE wechat_settings RENAME COLUMN bot_id TO agent_id;
ALTER TABLE feishu_settings RENAME COLUMN bot_id TO agent_id;
ALTER TABLE channel_commands RENAME COLUMN bot_id TO agent_id;
ALTER TABLE automation_tool_operations RENAME COLUMN bot_id TO agent_id;
ALTER TABLE automation_notification_targets RENAME COLUMN bot_id TO agent_id;
ALTER TABLE questions RENAME COLUMN bot_id TO agent_id;
ALTER TABLE questions RENAME COLUMN action_bot_id TO action_agent_id;
ALTER TABLE question_messages RENAME COLUMN bot_id TO agent_id;
ALTER TABLE telegram_album_members RENAME COLUMN bot_id TO agent_id;
ALTER TABLE telegram_effects RENAME COLUMN bot_id TO agent_id;
ALTER TABLE monitors RENAME COLUMN bot_id TO agent_id;
ALTER TABLE task_tool_operations RENAME COLUMN bot_id TO agent_id;
ALTER TABLE mcp_servers RENAME COLUMN bot_id TO agent_id;
ALTER TABLE telegram_managed_setups RENAME COLUMN bot_id TO agent_id;
ALTER TABLE channel_webhook_queue RENAME COLUMN bot_id TO agent_id;
ALTER TABLE web_notifications RENAME COLUMN bot_id TO agent_id;
ALTER TABLE telegram_callbacks RENAME COLUMN bot_id TO agent_id;
ALTER TABLE outbox_messages RENAME COLUMN bot_id TO agent_id;
ALTER TABLE outbox_files RENAME COLUMN bot_id TO agent_id;
ALTER TABLE wechat_settings RENAME COLUMN wechat_bot_id TO bot_id;
ALTER TABLE telegram_managed_setups RENAME COLUMN telegram_bot_id TO bot_id;

CREATE INDEX channel_command_delivery ON channel_commands (agent_id, completed, delivered_at);
CREATE INDEX telegram_album_collect ON telegram_album_members(agent_id, state, group_key, batch_source);
CREATE INDEX telegram_effects_source ON telegram_effects(agent_id, source_key);
CREATE INDEX telegram_managed_setups_agent ON telegram_managed_setups(agent_id, expires_at);
CREATE INDEX channel_webhook_pending ON "channel_webhook_queue"(agent_id, platform, account, id) WHERE payload IS NOT NULL;
CREATE INDEX outbox_messages_pending ON outbox_messages (agent_id, channel, peer) WHERE status = 'pending';
CREATE UNIQUE INDEX sessions_native ON sessions (agent_id, execution_id) WHERE execution_id IS NOT NULL;

CREATE VIEW configured_channel_addresses AS
SELECT s.agent_id, 'telegram' AS platform, substr(s.token, 1, instr(s.token, ':') - 1) AS account_id,
       'telegram:' || CAST(u.value AS INTEGER) AS peer
FROM telegram_settings s, json_each('[' || replace(json_quote(replace(s.allowed_users, ' ', '')), ',', '","') || ']') u
WHERE CAST(u.value AS INTEGER) > 0 AND CAST(CAST(u.value AS INTEGER) AS TEXT) = u.value
UNION ALL
SELECT agent_id, 'feishu', app_id, 'feishu:' || owner_id FROM feishu_settings WHERE owner_id <> ''
UNION ALL
SELECT agent_id, 'wechat', bot_id, 'wechat:' || owner FROM wechat_settings WHERE owner <> '';
CREATE VIEW cancel_unpaired_channel_work AS SELECT agent_id, platform FROM configured_channel_addresses WHERE 0;

-- +goose StatementBegin
CREATE TRIGGER agent_notification_target_update BEFORE UPDATE OF primary_notification_target_id ON agents
WHEN NEW.primary_notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM "automation_notification_targets" t WHERE t.id = NEW.primary_notification_target_id AND t.agent_id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_telegram_albums BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE telegram_album_members SET state = 'discarded'
    WHERE agent_id = OLD.agent_id AND state IN ('collecting', 'sealed');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER revoke_changed_telegram_albums AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE telegram_album_members SET state = 'discarded'
    WHERE agent_id = NEW.agent_id AND state IN ('collecting', 'sealed')
      AND (account_id <> substr(NEW.token, 1, instr(NEW.token, ':') - 1)
        OR instr(',' || replace(NEW.allowed_users, ' ', '') || ',', ',' || sender_id || ',') = 0);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_archived_telegram_albums AFTER UPDATE OF archived_at ON agents
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE telegram_album_members SET state = 'discarded'
    WHERE agent_id = NEW.id AND state IN ('collecting', 'sealed');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_deleted_telegram_effects BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE telegram_effects SET state='discarded', body=NULL
    WHERE agent_id=OLD.agent_id AND (state IN ('ready','processing','unknown') OR input_json<>'{}' OR body IS NOT NULL);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_revoked_telegram_effects AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE telegram_effects SET state='discarded', body=NULL
    WHERE agent_id=NEW.agent_id AND (state IN ('ready','processing','unknown') OR input_json<>'{}' OR body IS NOT NULL)
      AND (account_id<>substr(NEW.token,1,instr(NEW.token,':')-1)
        OR instr(','||replace(NEW.allowed_users,' ','')||',',','||sender_id||',')=0);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER discard_archived_telegram_effects AFTER UPDATE OF archived_at ON agents
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE telegram_effects SET state='discarded', body=NULL
    WHERE agent_id=NEW.id AND (state IN ('ready','processing','unknown') OR input_json<>'{}' OR body IS NOT NULL);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cancel_archived_agent_monitors AFTER UPDATE OF archived_at ON agents
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE monitors
    SET state = 'cancelled', outcome = 'agent archived', running = 0, updated_at = NEW.archived_at
    WHERE agent_id = NEW.id AND state = 'scheduled';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER telegram_managed_settings_insert AFTER INSERT ON telegram_settings BEGIN
    UPDATE telegram_managed_setups SET state = 'canceled' WHERE agent_id = NEW.agent_id AND state = 'pending';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER telegram_managed_settings_update AFTER UPDATE OF token, allowed_users, options ON telegram_settings BEGIN
    UPDATE telegram_managed_setups SET state = 'canceled' WHERE agent_id = NEW.agent_id AND state = 'pending';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER telegram_managed_settings_delete AFTER DELETE ON telegram_settings BEGIN
    UPDATE telegram_managed_setups SET state = 'canceled' WHERE agent_id = OLD.agent_id AND state = 'pending';
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
-- +goose StatementBegin
CREATE TRIGGER automation_notification_target_insert BEFORE INSERT ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM automation_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER automation_notification_target_update BEFORE UPDATE OF notification_target_id, agent_id ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM automation_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER clear_revoked_automation_target BEFORE DELETE ON automation_notification_targets
BEGIN
    UPDATE automations SET notification_route = 'none', notification_target_id = NULL
    WHERE agent_id = OLD.agent_id AND notification_target_id = OLD.id;
END;
-- +goose StatementEnd
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
    UPDATE channel_commands SET delivered_at = unixepoch() * 1000, answer = ''
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
    DELETE FROM automation_notification_targets
    WHERE agent_id = NEW.agent_id AND platform = NEW.platform AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.platform
          AND a.account_id = automation_notification_targets.account_id AND a.peer = automation_notification_targets.peer
    );
END;
-- +goose StatementEnd
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
