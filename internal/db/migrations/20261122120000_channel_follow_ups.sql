-- +goose Up
CREATE TABLE channel_follow_ups (
    delegation_id TEXT PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE,
    result_id TEXT NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'notified', 'silent', 'missed_follow_up', 'cancelled'))
) STRICT;
CREATE INDEX channel_follow_ups_parent ON channel_follow_ups(conversation_id);

-- Read the current grant from the original receipt and settings, not from a copied identity.
CREATE VIEW authorized_channel_follow_up_sources AS
SELECT s.id, c.agent_id, s.origin AS peer
FROM submissions s JOIN conversations c ON c.id = s.conversation_id
WHERE s.kind = 'message' AND s.channel_revoked = 0 AND c.execution_scope = ''
AND (
    (s.origin LIKE 'telegram:%' AND EXISTS (
        SELECT 1 FROM telegram_settings t WHERE t.agent_id = c.agent_id
        AND instr(s.source_key, 'telegram:' || substr(t.token, 1, instr(t.token, ':') - 1) || ':') = 1
        AND instr(',' || replace(t.allowed_users, ' ', '') || ',',
            ',' || coalesce(json_extract(s.channel_context, '$.senderId'), substr(s.origin, 10)) || ',') > 0
    )) OR (s.origin LIKE 'feishu:%' AND EXISTS (
        SELECT 1 FROM feishu_settings f WHERE f.agent_id = c.agent_id
        AND instr(s.source_key, 'feishu:' || f.app_id || ':') = 1 AND s.origin = 'feishu:' || f.owner_id
    )) OR (s.origin LIKE 'wechat:%' AND EXISTS (
        SELECT 1 FROM wechat_settings w WHERE w.agent_id = c.agent_id
        AND instr(s.source_key, 'wechat:' || w.bot_id || ':') = 1 AND s.origin = 'wechat:' || w.owner
    ))
);

-- The delegation already points to the trusted initiating request. Do not copy its identity.
-- A notification may still be waiting in the Outbox after the result turn ends.
CREATE VIEW pending_channel_follow_ups AS
SELECT f.delegation_id, f.conversation_id, d.conversation_id AS child_id,
       c.agent_id, s.id AS source_id, f.result_id, s.origin AS peer
FROM channel_follow_ups f
JOIN submissions d ON d.id = f.delegation_id
JOIN submissions s ON s.id = d.parent_request_id
JOIN conversations c ON c.id = f.conversation_id
WHERE f.state <> 'cancelled' AND (f.state = 'open' OR EXISTS (
    SELECT 1 FROM outbox_messages o WHERE o.source_submission_id = f.result_id AND o.status = 'pending'
));

-- +goose StatementBegin
CREATE TRIGGER cancel_channel_follow_up_outbox AFTER UPDATE OF state ON channel_follow_ups
WHEN NEW.state = 'cancelled' AND OLD.state <> 'cancelled'
BEGIN
    UPDATE outbox_messages SET status = 'revoked'
    WHERE source_submission_id = NEW.result_id AND status = 'pending';
END;
-- +goose StatementEnd

-- The original ack can already be delivered, before the result exists. Remember revocation
-- when it happens, so re-pairing cannot revive a commitment to an earlier grant.

-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_follow_ups_on_delete AFTER DELETE ON telegram_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled' WHERE delegation_id IN (
        SELECT delegation_id FROM pending_channel_follow_ups p
        WHERE p.agent_id = OLD.agent_id AND peer LIKE 'telegram:%'
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_telegram_follow_ups_on_update AFTER UPDATE OF token, allowed_users ON telegram_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled' WHERE delegation_id IN (
        SELECT delegation_id FROM pending_channel_follow_ups p
        WHERE p.agent_id = NEW.agent_id AND peer LIKE 'telegram:%'
          AND NOT EXISTS (SELECT 1 FROM authorized_channel_follow_up_sources a WHERE a.id = p.source_id)
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_follow_ups_on_delete AFTER DELETE ON feishu_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled' WHERE delegation_id IN (
        SELECT delegation_id FROM pending_channel_follow_ups p
        WHERE p.agent_id = OLD.agent_id AND peer LIKE 'feishu:%'
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_feishu_follow_ups_on_update AFTER UPDATE OF app_id, owner_id ON feishu_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled' WHERE delegation_id IN (
        SELECT delegation_id FROM pending_channel_follow_ups p
        WHERE p.agent_id = NEW.agent_id AND peer LIKE 'feishu:%'
          AND NOT EXISTS (SELECT 1 FROM authorized_channel_follow_up_sources a WHERE a.id = p.source_id)
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_follow_ups_on_delete AFTER DELETE ON wechat_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled' WHERE delegation_id IN (
        SELECT delegation_id FROM pending_channel_follow_ups p
        WHERE p.agent_id = OLD.agent_id AND peer LIKE 'wechat:%'
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cancel_wechat_follow_ups_on_update AFTER UPDATE OF bot_id, owner ON wechat_settings
BEGIN
    UPDATE channel_follow_ups SET state = 'cancelled' WHERE delegation_id IN (
        SELECT delegation_id FROM pending_channel_follow_ups p
        WHERE p.agent_id = NEW.agent_id AND peer LIKE 'wechat:%'
          AND NOT EXISTS (SELECT 1 FROM authorized_channel_follow_up_sources a WHERE a.id = p.source_id)
    );
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cancel_wechat_follow_ups_on_update;
DROP TRIGGER cancel_wechat_follow_ups_on_delete;
DROP TRIGGER cancel_feishu_follow_ups_on_update;
DROP TRIGGER cancel_feishu_follow_ups_on_delete;
DROP TRIGGER cancel_telegram_follow_ups_on_update;
DROP TRIGGER cancel_telegram_follow_ups_on_delete;
DROP TRIGGER cancel_channel_follow_up_outbox;
DROP VIEW pending_channel_follow_ups;
DROP VIEW authorized_channel_follow_up_sources;
DROP TABLE channel_follow_ups;
