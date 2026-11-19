-- +goose Up
-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_feishu_outbox AFTER DELETE ON feishu_settings
BEGIN
    UPDATE outbox_messages
    SET status = 'revoked', error = 'Feishu authorization revoked'
    WHERE agent_id = OLD.agent_id AND status = 'pending' AND peer LIKE 'feishu:%';
    UPDATE submissions
    SET channel_revoked = 1, channel_delivery_error = 'Feishu authorization revoked'
    WHERE delivered_at IS NULL AND origin LIKE 'feishu:%'
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
    WHERE delivered_at IS NULL AND origin LIKE 'feishu:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = NEW.agent_id)
      AND (OLD.app_id <> NEW.app_id OR NEW.owner_id = '' OR origin <> 'feishu:' || NEW.owner_id);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS revoke_changed_feishu_outbox;
DROP TRIGGER IF EXISTS revoke_deleted_feishu_outbox;
