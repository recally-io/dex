-- +goose Up
CREATE TABLE routine_notification_targets (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('telegram', 'feishu', 'wechat')),
    account_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    UNIQUE (id, agent_id),
    UNIQUE (agent_id, platform, account_id, peer)
) STRICT;

ALTER TABLE agents ADD COLUMN primary_notification_target_id TEXT REFERENCES routine_notification_targets(id) ON DELETE SET NULL;
ALTER TABLE automations ADD COLUMN notification_route TEXT NOT NULL DEFAULT 'none' CHECK (notification_route IN ('none', 'default', 'target'));
ALTER TABLE automations ADD COLUMN notification_target_id TEXT REFERENCES routine_notification_targets(id) ON DELETE SET NULL;

CREATE TRIGGER routine_notification_target_identity BEFORE UPDATE ON routine_notification_targets
BEGIN SELECT RAISE(ABORT, 'notification targets are immutable'); END;

CREATE TRIGGER routine_notification_target_insert BEFORE INSERT ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
CREATE TRIGGER routine_notification_target_update BEFORE UPDATE OF notification_target_id, agent_id ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
CREATE TRIGGER agent_notification_target_update BEFORE UPDATE OF primary_notification_target_id ON agents
WHEN NEW.primary_notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.primary_notification_target_id AND t.agent_id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;

CREATE TABLE routine_notification_deliveries (
    run_id TEXT PRIMARY KEY REFERENCES automation_runs(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    account_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    body TEXT NOT NULL,
    rendered_parts TEXT NOT NULL DEFAULT '',
    parts INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'rejected')),
    reason TEXT NOT NULL DEFAULT ''
) STRICT;

-- Revoked destinations cannot be revived by reconnecting the same account later.
-- +goose StatementBegin
CREATE TRIGGER reject_revoked_notification_target BEFORE DELETE ON routine_notification_targets
BEGIN
    UPDATE routine_notification_deliveries SET status = 'rejected', reason = 'destination no longer authorized'
    WHERE target_id = OLD.id AND status = 'pending';
    UPDATE automations SET notification_route = 'none', notification_target_id = NULL
    WHERE agent_id = OLD.agent_id AND notification_target_id = OLD.id;
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

-- +goose Down
DROP TRIGGER revoke_changed_wechat_targets;
DROP TRIGGER revoke_deleted_wechat_targets;
DROP TRIGGER revoke_changed_feishu_targets;
DROP TRIGGER revoke_deleted_feishu_targets;
DROP TRIGGER revoke_changed_telegram_targets;
DROP TRIGGER revoke_deleted_telegram_targets;
DROP TRIGGER reject_revoked_notification_target;
DROP TABLE routine_notification_deliveries;
DROP TRIGGER agent_notification_target_update;
DROP TRIGGER routine_notification_target_update;
DROP TRIGGER routine_notification_target_insert;
DROP TRIGGER routine_notification_target_identity;
ALTER TABLE automations DROP COLUMN notification_target_id;
ALTER TABLE automations DROP COLUMN notification_route;
ALTER TABLE agents DROP COLUMN primary_notification_target_id;
DROP TABLE routine_notification_targets;
