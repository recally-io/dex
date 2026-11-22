-- +goose Up
CREATE TABLE channel_follow_ups (
    delegation_id TEXT PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    child_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    peer TEXT NOT NULL,
    account_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    channel_context TEXT NOT NULL,
    result_id TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'intentional_silence', 'missed_follow_up', 'queued', 'cancelled'))
) STRICT;
CREATE INDEX channel_follow_ups_source ON channel_follow_ups(source_id);
CREATE INDEX channel_follow_ups_parent ON channel_follow_ups(conversation_id);
CREATE INDEX channel_follow_ups_child ON channel_follow_ups(child_id);
CREATE INDEX channel_follow_ups_agent ON channel_follow_ups(agent_id, peer);

-- +goose StatementBegin
CREATE TRIGGER cancel_channel_follow_up_outbox AFTER UPDATE OF state ON channel_follow_ups
WHEN NEW.state = 'cancelled' AND OLD.state <> 'cancelled'
BEGIN
    UPDATE outbox_messages SET status = 'revoked'
    WHERE status = 'pending' AND source_submission_id = NEW.result_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_revoked_channel_follow_ups AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.channel_revoked <> 0 AND OLD.channel_revoked = 0
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled'
    WHERE (source_id = NEW.id OR result_id = NEW.id)
      AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
        AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = result_id AND status = 'pending')))
      AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || delegation_id AND status = 'delivered');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_follow_ups_on_delete AFTER DELETE ON telegram_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled'
    WHERE agent_id = OLD.agent_id AND peer LIKE 'telegram:%' AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
        AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = result_id AND status = 'pending')))
      AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || delegation_id AND status = 'delivered');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_follow_ups_on_change AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled'
    WHERE agent_id = NEW.agent_id AND peer LIKE 'telegram:%' AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
        AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = result_id AND status = 'pending')))
      AND (account_id <> substr(NEW.token, 1, instr(NEW.token, ':') - 1) OR instr(',' || replace(NEW.allowed_users, ' ', '') || ',', ',' || sender_id || ',') = 0)
      AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || delegation_id AND status = 'delivered');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_follow_ups_on_delete AFTER DELETE ON feishu_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled'
    WHERE agent_id = OLD.agent_id AND peer LIKE 'feishu:%' AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
        AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = result_id AND status = 'pending')))
      AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || delegation_id AND status = 'delivered');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_follow_ups_on_change AFTER UPDATE OF app_id, owner_id ON feishu_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled'
    WHERE agent_id = NEW.agent_id AND peer LIKE 'feishu:%' AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
        AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = result_id AND status = 'pending')))
      AND (account_id <> NEW.app_id OR sender_id <> NEW.owner_id)
      AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || delegation_id AND status = 'delivered');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_follow_ups_on_delete AFTER DELETE ON wechat_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled'
    WHERE agent_id = OLD.agent_id AND peer LIKE 'wechat:%' AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
        AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = result_id AND status = 'pending')))
      AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || delegation_id AND status = 'delivered');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_follow_ups_on_change AFTER UPDATE OF bot_id, owner ON wechat_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled'
    WHERE agent_id = NEW.agent_id AND peer LIKE 'wechat:%' AND (state IN ('open', 'queued') OR (state IN ('intentional_silence', 'missed_follow_up')
        AND EXISTS (SELECT 1 FROM outbox_messages WHERE source_submission_id = result_id AND status = 'pending')))
      AND (account_id <> NEW.bot_id OR sender_id <> NEW.owner)
      AND NOT EXISTS (SELECT 1 FROM outbox_messages WHERE dedup_key = 'follow-up-final:' || delegation_id AND status = 'delivered');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cancel_wechat_follow_ups_on_change;
DROP TRIGGER cancel_wechat_follow_ups_on_delete;
DROP TRIGGER cancel_feishu_follow_ups_on_change;
DROP TRIGGER cancel_feishu_follow_ups_on_delete;
DROP TRIGGER cancel_telegram_follow_ups_on_change;
DROP TRIGGER cancel_telegram_follow_ups_on_delete;
DROP TRIGGER cancel_revoked_channel_follow_ups;
DROP TRIGGER cancel_channel_follow_up_outbox;
DROP TABLE channel_follow_ups;
