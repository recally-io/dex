-- +goose Up
ALTER TABLE telegram_settings ADD COLUMN options TEXT NOT NULL DEFAULT '{}';

-- Provider and control receipts prevent replay from repeating an unknown remote side effect.
CREATE TABLE telegram_effects (
    operation_key TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    source_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    input_json TEXT NOT NULL DEFAULT '{}',
    state TEXT NOT NULL CHECK (state IN ('ready', 'processing', 'done', 'failed', 'unknown', 'discarded')),
    text TEXT NOT NULL DEFAULT '',
    body BLOB CHECK (length(body) <= 20971520),
    name TEXT NOT NULL DEFAULT '',
    result_json TEXT NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX telegram_effects_source ON telegram_effects(agent_id, source_key);

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

-- +goose Down
DROP TRIGGER discard_archived_telegram_effects;
DROP TRIGGER discard_revoked_telegram_effects;
DROP TRIGGER discard_deleted_telegram_effects;
DROP TABLE telegram_effects;
ALTER TABLE telegram_settings DROP COLUMN options;
