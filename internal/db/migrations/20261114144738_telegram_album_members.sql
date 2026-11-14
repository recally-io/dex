-- +goose Up
-- These are platform member receipts and temporary upload references, not a native work queue.
CREATE TABLE telegram_album_members (
    source_key TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    group_key TEXT NOT NULL,
    input_json TEXT NOT NULL,
    received_at INTEGER NOT NULL,
    batch_source TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'collecting' CHECK (state IN ('collecting', 'sealed', 'transferred', 'discarded'))
) STRICT;
CREATE INDEX telegram_album_collect ON telegram_album_members(agent_id, state, group_key, batch_source);

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

-- +goose Down
DROP TRIGGER discard_archived_telegram_albums;
DROP TRIGGER revoke_changed_telegram_albums;
DROP TRIGGER revoke_deleted_telegram_albums;
DROP TABLE telegram_album_members;
