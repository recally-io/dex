-- +goose Up
CREATE TABLE telegram_manager (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    credential_ref TEXT NOT NULL
) STRICT;

CREATE TABLE telegram_managed_setups (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    manager_id INTEGER NOT NULL,
    username TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    owner_id INTEGER NOT NULL DEFAULT 0,
    bot_id INTEGER UNIQUE,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'connected', 'failed', 'canceled'))
) STRICT;

CREATE INDEX telegram_managed_setups_agent ON telegram_managed_setups(agent_id, expires_at);

-- A manual save, pairing or removal supersedes an unfinished setup.
-- Managed completion sets its state before it saves the channel in the same transaction.
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

-- +goose Down
DROP TRIGGER telegram_managed_settings_insert;
DROP TRIGGER telegram_managed_settings_update;
DROP TRIGGER telegram_managed_settings_delete;
-- A build without the private vault cannot use these credentials.
DELETE FROM telegram_settings WHERE token GLOB '*::vault:*';
DROP TABLE telegram_managed_setups;
DROP TABLE telegram_manager;
