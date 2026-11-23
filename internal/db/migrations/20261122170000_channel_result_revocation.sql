-- +goose Up
-- Keep a revocation on the source receipt even after its direct reply was delivered.
-- CreateTurnResult inherits this flag for every child result, not only promised follow-ups.
DROP TRIGGER revoke_deleted_telegram_replies;
DROP TRIGGER revoke_changed_telegram_replies;
DROP TRIGGER revoke_deleted_feishu_outbox;
DROP TRIGGER revoke_changed_feishu_outbox;

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

-- A result that already exists shares the same channel scope. Revoke an unsent Outbox row
-- immediately; a platform response for a send already in progress still counts.
-- +goose StatementBegin
CREATE TRIGGER revoke_outbox_on_channel_result_revocation AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.kind = 'task_result' AND OLD.channel_revoked = 0 AND NEW.channel_revoked <> 0
BEGIN
    UPDATE outbox_messages SET status = 'revoked', error = NEW.channel_delivery_error
    WHERE source_submission_id = NEW.id AND status = 'pending';
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER revoke_outbox_on_channel_result_revocation;
DROP TRIGGER revoke_changed_wechat_replies;
DROP TRIGGER revoke_deleted_wechat_replies;
DROP TRIGGER revoke_deleted_telegram_replies;
DROP TRIGGER revoke_changed_telegram_replies;
DROP TRIGGER revoke_deleted_feishu_outbox;
DROP TRIGGER revoke_changed_feishu_outbox;

-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_telegram_replies BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'Telegram authorization revoked'
    WHERE delivered_at IS NULL AND origin LIKE 'telegram:%'
      AND conversation_id IN (SELECT id FROM conversations WHERE agent_id = OLD.agent_id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_changed_telegram_replies AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE submissions SET channel_revoked = 1, channel_delivery_error = 'Telegram authorization revoked'
    WHERE delivered_at IS NULL AND origin LIKE 'telegram:%'
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
