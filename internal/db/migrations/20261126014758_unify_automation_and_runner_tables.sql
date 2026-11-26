-- +goose Up
-- The outbox already owns delivery. Down restores only the retired schema.
DROP TRIGGER reject_revoked_notification_target;
DROP TABLE routine_notification_deliveries;
DROP TRIGGER release_channel_reply_files;
DROP INDEX channel_reply_files_submission;
DROP TABLE channel_reply_files;

DROP TRIGGER routine_notification_target_identity;
DROP TRIGGER routine_notification_target_insert;
DROP TRIGGER routine_notification_target_update;
ALTER TABLE routine_tool_operations RENAME TO automation_tool_operations;
ALTER TABLE routine_notification_targets RENAME TO automation_notification_targets;
ALTER TABLE execution_environments RENAME TO runners;
ALTER TABLE agents RENAME COLUMN environment_id TO runner_id;
ALTER TABLE agents RENAME COLUMN environment_sandbox_id TO runner_sandbox_id;
ALTER TABLE conversations RENAME COLUMN environment_id TO runner_id;
ALTER TABLE automations RENAME COLUMN environment_id TO runner_id;
ALTER TABLE automation_runs RENAME COLUMN environment_id TO runner_id;
ALTER TABLE runner_pairing RENAME COLUMN environment_id TO runner_id;
ALTER TABLE monitors RENAME COLUMN target_environment TO target_runner_id;
ALTER TABLE monitors RENAME COLUMN target_environment_revision TO target_runner_revision;

-- +goose StatementBegin
CREATE TRIGGER automation_notification_target_identity BEFORE UPDATE ON automation_notification_targets
BEGIN SELECT RAISE(ABORT, 'notification targets are immutable'); END;
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

-- Keep the pinned Runner in saved tool results that the service can replay.
UPDATE automation_tool_operations
SET result = json_remove(json_set(result, '$.runnerId', result ->> '$.environmentId'), '$.environmentId')
WHERE action IN ('create', 'update') AND json_type(result, '$.environmentId') = 'text';

-- Copy both tables before dropping the old parent to avoid cascading file deletes.
-- Recreate delivery triggers after the copy so saved delivery state does not change.
CREATE TABLE outbox_messages_new (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_submission_id TEXT REFERENCES submissions (id),
    kind TEXT NOT NULL CHECK (kind IN ('reply', 'proactive', 'question', 'automation', 'system_error', 'file', 'control', 'feedback')),
    peer TEXT NOT NULL CHECK (instr(peer, ':') > 1 AND instr(peer, ':') < length(peer)),
    channel TEXT GENERATED ALWAYS AS (CASE WHEN instr(peer, ':') > 0 THEN substr(peer, 1, instr(peer, ':') - 1) ELSE '' END) STORED,
    target TEXT GENERATED ALWAYS AS (CASE WHEN instr(peer, ':') > 0 THEN substr(peer, instr(peer, ':') + 1) ELSE '' END) STORED,
    channel_context TEXT NOT NULL DEFAULT '{}',
    topic_key TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    rendered_message TEXT NOT NULL DEFAULT '',
    remote_ids TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'superseded', 'failed')),
    retry_at INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    delivered_at INTEGER,
    UNIQUE (event_id, channel, target)
) STRICT;
CREATE TABLE outbox_files_new (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    operation_key TEXT NOT NULL,
    submission_id TEXT REFERENCES submissions (id) ON DELETE CASCADE,
    outbox_id TEXT REFERENCES outbox_messages_new (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    body BLOB NOT NULL CHECK (length(body) <= 20971520),
    UNIQUE (agent_id, operation_key)
) STRICT;

INSERT INTO outbox_messages_new (id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at)
SELECT id, event_id, agent_id, conversation_id, source_submission_id, CASE kind WHEN 'routine' THEN 'automation' ELSE kind END, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at
FROM outbox_messages;
INSERT INTO outbox_files_new (id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body)
SELECT id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body FROM outbox_files;

DROP TRIGGER release_unanswered_outbox_files;
DROP TRIGGER cancel_unpaired_channel_work;
DROP TRIGGER revoke_outbox_on_channel_result_revocation;
DROP VIEW outbox_delivery_diagnostics;
DROP TABLE outbox_files;
DROP TABLE outbox_messages;
ALTER TABLE outbox_messages_new RENAME TO outbox_messages;
ALTER TABLE outbox_files_new RENAME TO outbox_files;

CREATE INDEX outbox_messages_pending ON outbox_messages (agent_id, channel, peer) WHERE status = 'pending';
CREATE INDEX outbox_messages_source ON outbox_messages (source_submission_id) WHERE source_submission_id IS NOT NULL;
CREATE INDEX outbox_messages_conversation ON outbox_messages (conversation_id, created_at DESC);
CREATE INDEX outbox_files_submission ON outbox_files (submission_id) WHERE submission_id IS NOT NULL;
CREATE UNIQUE INDEX outbox_files_outbox ON outbox_files (outbox_id) WHERE outbox_id IS NOT NULL;

-- +goose StatementBegin
CREATE TRIGGER release_unanswered_outbox_files AFTER UPDATE OF status ON submissions
WHEN NEW.status = 'unanswered'
BEGIN DELETE FROM outbox_files WHERE submission_id = NEW.id AND outbox_id IS NULL; END;
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
    UPDATE channel_commands SET delivered_at = unixepoch() * 1000, answer = '', rendered_reply = ''
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
CREATE TRIGGER revoke_outbox_on_channel_result_revocation AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.kind = 'task_result' AND OLD.channel_revoked = 0 AND NEW.channel_revoked <> 0
BEGIN
  UPDATE outbox_messages SET status = 'failed', error = NEW.channel_delivery_error, retry_at = 0
  WHERE source_submission_id = NEW.id AND status = 'pending';
END;
-- +goose StatementEnd
CREATE VIEW outbox_delivery_diagnostics AS
SELECT o.id, o.source_submission_id, o.peer, o.remote_ids,
    CASE
        WHEN o.peer <> '' AND EXISTS (
            SELECT 1 FROM json_each(o.remote_ids) r WHERE coalesce(r.value ->> 'id', '') <> ''
        ) THEN 'platform_confirmed'
        WHEN o.status = 'failed' THEN 'revoked_or_skipped_unconfirmed'
        ELSE 'unconfirmed'
    END AS disposition
FROM outbox_messages o;
-- +goose StatementBegin
CREATE TRIGGER release_outbox_files AFTER UPDATE OF status ON outbox_messages
WHEN NEW.status != 'pending'
BEGIN DELETE FROM outbox_files WHERE outbox_id = NEW.id; END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER finish_outbox_command AFTER UPDATE OF status ON outbox_messages
WHEN NEW.kind IN ('control', 'feedback') AND NEW.status IN ('sent', 'failed')
BEGIN
    UPDATE channel_commands SET delivered_at = coalesce(NEW.delivered_at, unixepoch() * 1000),
        answer = CASE WHEN NEW.kind = 'feedback' THEN '' ELSE answer END,
        rendered_reply = CASE WHEN NEW.kind = 'feedback' THEN '' ELSE rendered_reply END
    WHERE source_key = NEW.id;
    UPDATE outbox_messages SET rendered_message = '' WHERE id = NEW.id AND NEW.kind = 'feedback';
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

-- +goose Down
DROP TRIGGER clear_revoked_automation_target;

UPDATE automation_tool_operations
SET result = json_remove(json_set(result, '$.environmentId', result ->> '$.runnerId'), '$.runnerId')
WHERE action IN ('create', 'update') AND json_type(result, '$.runnerId') = 'text';

DROP TRIGGER automation_notification_target_identity;
DROP TRIGGER automation_notification_target_insert;
DROP TRIGGER automation_notification_target_update;
ALTER TABLE automation_tool_operations RENAME TO routine_tool_operations;
ALTER TABLE automation_notification_targets RENAME TO routine_notification_targets;
ALTER TABLE runners RENAME TO execution_environments;
ALTER TABLE agents RENAME COLUMN runner_id TO environment_id;
ALTER TABLE agents RENAME COLUMN runner_sandbox_id TO environment_sandbox_id;
ALTER TABLE conversations RENAME COLUMN runner_id TO environment_id;
ALTER TABLE automations RENAME COLUMN runner_id TO environment_id;
ALTER TABLE automation_runs RENAME COLUMN runner_id TO environment_id;
ALTER TABLE runner_pairing RENAME COLUMN runner_id TO environment_id;
ALTER TABLE monitors RENAME COLUMN target_runner_id TO target_environment;
ALTER TABLE monitors RENAME COLUMN target_runner_revision TO target_environment_revision;

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

CREATE TABLE channel_reply_files (
    operation_key TEXT PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    body BLOB NOT NULL CHECK (length(body) <= 20971520)
) STRICT;
CREATE INDEX channel_reply_files_submission ON channel_reply_files(submission_id);

-- +goose StatementBegin
CREATE TRIGGER release_channel_reply_files AFTER UPDATE OF delivered_at, channel_revoked, status ON submissions
WHEN NEW.delivered_at IS NOT NULL OR NEW.channel_revoked = 1 OR NEW.status = 'unanswered'
BEGIN
    DELETE FROM channel_reply_files WHERE submission_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER routine_notification_target_identity BEFORE UPDATE ON routine_notification_targets
BEGIN SELECT RAISE(ABORT, 'notification targets are immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER routine_notification_target_insert BEFORE INSERT ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER routine_notification_target_update BEFORE UPDATE OF notification_target_id, agent_id ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
-- +goose StatementEnd

-- Copy both tables before dropping the old parent to avoid cascading file deletes.
-- Recreate delivery triggers after the copy so saved delivery state does not change.
CREATE TABLE outbox_messages_new (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_submission_id TEXT REFERENCES submissions (id),
    kind TEXT NOT NULL CHECK (kind IN ('reply', 'proactive', 'question', 'routine', 'system_error', 'file', 'control', 'feedback')),
    peer TEXT NOT NULL CHECK (instr(peer, ':') > 1 AND instr(peer, ':') < length(peer)),
    channel TEXT GENERATED ALWAYS AS (CASE WHEN instr(peer, ':') > 0 THEN substr(peer, 1, instr(peer, ':') - 1) ELSE '' END) STORED,
    target TEXT GENERATED ALWAYS AS (CASE WHEN instr(peer, ':') > 0 THEN substr(peer, instr(peer, ':') + 1) ELSE '' END) STORED,
    channel_context TEXT NOT NULL DEFAULT '{}',
    topic_key TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    rendered_message TEXT NOT NULL DEFAULT '',
    remote_ids TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'superseded', 'failed')),
    retry_at INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    delivered_at INTEGER,
    UNIQUE (event_id, channel, target)
) STRICT;
CREATE TABLE outbox_files_new (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    operation_key TEXT NOT NULL,
    submission_id TEXT REFERENCES submissions (id) ON DELETE CASCADE,
    outbox_id TEXT REFERENCES outbox_messages_new (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    body BLOB NOT NULL CHECK (length(body) <= 20971520),
    UNIQUE (agent_id, operation_key)
) STRICT;

INSERT INTO outbox_messages_new (id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at)
SELECT id, event_id, agent_id, conversation_id, source_submission_id, CASE kind WHEN 'automation' THEN 'routine' ELSE kind END, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at
FROM outbox_messages;
INSERT INTO outbox_files_new (id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body)
SELECT id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body FROM outbox_files;

DROP TRIGGER release_unanswered_outbox_files;
DROP TRIGGER cancel_unpaired_channel_work;
DROP TRIGGER revoke_outbox_on_channel_result_revocation;
DROP VIEW outbox_delivery_diagnostics;
DROP TABLE outbox_files;
DROP TABLE outbox_messages;
ALTER TABLE outbox_messages_new RENAME TO outbox_messages;
ALTER TABLE outbox_files_new RENAME TO outbox_files;

CREATE INDEX outbox_messages_pending ON outbox_messages (agent_id, channel, peer) WHERE status = 'pending';
CREATE INDEX outbox_messages_source ON outbox_messages (source_submission_id) WHERE source_submission_id IS NOT NULL;
CREATE INDEX outbox_messages_conversation ON outbox_messages (conversation_id, created_at DESC);
CREATE INDEX outbox_files_submission ON outbox_files (submission_id) WHERE submission_id IS NOT NULL;
CREATE UNIQUE INDEX outbox_files_outbox ON outbox_files (outbox_id) WHERE outbox_id IS NOT NULL;

-- +goose StatementBegin
CREATE TRIGGER release_unanswered_outbox_files AFTER UPDATE OF status ON submissions
WHEN NEW.status = 'unanswered'
BEGIN DELETE FROM outbox_files WHERE submission_id = NEW.id AND outbox_id IS NULL; END;
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
    UPDATE channel_commands SET delivered_at = unixepoch() * 1000, answer = '', rendered_reply = ''
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
    DELETE FROM routine_notification_targets
    WHERE agent_id = NEW.agent_id AND platform = NEW.platform AND NOT EXISTS (
        SELECT 1 FROM configured_channel_addresses a
        WHERE a.agent_id = NEW.agent_id AND a.platform = NEW.platform
          AND a.account_id = routine_notification_targets.account_id AND a.peer = routine_notification_targets.peer
    );
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER revoke_outbox_on_channel_result_revocation AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.kind = 'task_result' AND OLD.channel_revoked = 0 AND NEW.channel_revoked <> 0
BEGIN
  UPDATE outbox_messages SET status = 'failed', error = NEW.channel_delivery_error, retry_at = 0
  WHERE source_submission_id = NEW.id AND status = 'pending';
END;
-- +goose StatementEnd
CREATE VIEW outbox_delivery_diagnostics AS
SELECT o.id, o.source_submission_id, o.peer, o.remote_ids,
    CASE
        WHEN o.peer <> '' AND EXISTS (
            SELECT 1 FROM json_each(o.remote_ids) r WHERE coalesce(r.value ->> 'id', '') <> ''
        ) THEN 'platform_confirmed'
        WHEN o.status = 'failed' THEN 'revoked_or_skipped_unconfirmed'
        ELSE 'unconfirmed'
    END AS disposition
FROM outbox_messages o;
-- +goose StatementBegin
CREATE TRIGGER release_outbox_files AFTER UPDATE OF status ON outbox_messages
WHEN NEW.status != 'pending'
BEGIN DELETE FROM outbox_files WHERE outbox_id = NEW.id; END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER finish_outbox_command AFTER UPDATE OF status ON outbox_messages
WHEN NEW.kind IN ('control', 'feedback') AND NEW.status IN ('sent', 'failed')
BEGIN
    UPDATE channel_commands SET delivered_at = coalesce(NEW.delivered_at, unixepoch() * 1000),
        answer = CASE WHEN NEW.kind = 'feedback' THEN '' ELSE answer END,
        rendered_reply = CASE WHEN NEW.kind = 'feedback' THEN '' ELSE rendered_reply END
    WHERE source_key = NEW.id;
    UPDATE outbox_messages SET rendered_message = '' WHERE id = NEW.id AND NEW.kind = 'feedback';
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
