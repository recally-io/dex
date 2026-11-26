-- +goose Up
-- Pre-scope Telegram groups used the agent store. Keep those native IDs for
-- startup isolation. Scoped groups used separate stores, which no Harness opens
-- now. Their IDs can match a private session and must not be used for recovery.
UPDATE conversations
SET channel_key = 'retired-telegram:' || id,
    archived_at = coalesce(archived_at, unixepoch() * 1000),
    abort_pending = CASE
        WHEN execution_scope <> 'retired-telegram-legacy' OR execution_id IS NULL THEN 0
        WHEN channel_key LIKE 'retired-telegram:%' THEN abort_pending
        ELSE 1 END,
    execution_id = CASE WHEN execution_scope = 'retired-telegram-legacy' THEN execution_id END
WHERE execution_scope <> '';

DROP INDEX conversations_native_scope;
ALTER TABLE conversations DROP COLUMN scope_context;
ALTER TABLE conversations DROP COLUMN execution_scope;
CREATE UNIQUE INDEX conversations_native ON conversations (agent_id, execution_id)
    WHERE execution_id IS NOT NULL;

-- Use keys outside the UUID range of new chats. Keep the parent and native links.
UPDATE conversations SET channel_key = 'chat:legacy:' || id
WHERE channel_key LIKE 'task:%' OR channel_key LIKE 'web:%';

UPDATE submissions SET status = 'unanswered', reason = 'Timer retired',
    settled_at = unixepoch() * 1000, stop_requested = 0
WHERE status = 'pending' AND producer = 'scheduler' AND kind = 'task_result'
    AND id LIKE 'timer:%' AND source_key = id;

-- Failure notifications compare the saved reasons of consecutive runs.
UPDATE automation_runs SET reason = CASE
    WHEN reason = 'the target session is missing or cannot accept routine inputs'
        THEN 'the target session is missing or cannot accept Automation inputs'
    ELSE 'the Runner ' || substr(reason, length('the environment ') + 1) END
WHERE status = 'skipped' AND (reason = 'the target session is missing or cannot accept routine inputs'
    OR reason GLOB 'the environment * is unavailable' OR reason GLOB 'the environment * was removed');

DROP INDEX task_entries_task_seq;
-- Keep dependent rows, foreign keys, and rowids when replacing the checks.
ALTER TABLE submissions ADD COLUMN exit_action_new TEXT NOT NULL DEFAULT ''
    CHECK (exit_action_new IN ('', 'notify_user', 'ask_user'));
UPDATE submissions SET exit_action_new = CASE WHEN exit_action IN ('stay_silent', 'report_parent') THEN '' ELSE exit_action END;
ALTER TABLE submissions DROP COLUMN exit_action;
ALTER TABLE submissions RENAME COLUMN exit_action_new TO exit_action;

DROP INDEX task_execution_lease;
ALTER TABLE task_executions ADD COLUMN state_new TEXT NOT NULL DEFAULT 'running'
    CHECK (state_new IN ('running', 'completed', 'failed', 'stale', 'cancelled'));
-- A retired state is not an active lease. Do not authorize a saved token to write.
UPDATE task_executions SET state_new = CASE WHEN state IN ('dispatched', 'idle') THEN 'stale' ELSE state END,
    ended_at = CASE WHEN state IN ('dispatched', 'idle') THEN coalesce(ended_at, unixepoch() * 1000) ELSE ended_at END;
ALTER TABLE task_executions DROP COLUMN state;
ALTER TABLE task_executions RENAME COLUMN state_new TO state;
CREATE INDEX task_execution_lease ON task_executions(lease_expires_at)
    WHERE kind = 'external_agent' AND state = 'running';

DROP TRIGGER revoke_outbox_on_channel_result_revocation;
ALTER TABLE submissions DROP COLUMN channel_delivery_error;
DROP TRIGGER release_unanswered_outbox_files;
DROP TRIGGER cancel_unpaired_channel_work;
DROP VIEW outbox_delivery_diagnostics;
DROP TRIGGER finish_outbox_command;
ALTER TABLE channel_commands DROP COLUMN rendered_reply;

-- Only private single-message callback slots can still be used.
CREATE TABLE telegram_callbacks_new (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    peer TEXT NOT NULL,
    channel_context TEXT NOT NULL,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    execution_id INTEGER NOT NULL,
    context_revision TEXT NOT NULL,
    label TEXT NOT NULL,
    object_id TEXT NOT NULL,
    action TEXT NOT NULL,
    message_id TEXT NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    UNIQUE(source_id, ordinal)
) STRICT;
INSERT INTO telegram_callbacks_new (id, agent_id, source_id, ordinal, peer, channel_context, conversation_id, execution_id,
    context_revision, label, object_id, action, message_id, expires_at, consumed_at)
SELECT id, agent_id, source_id, ordinal, peer, channel_context, conversation_id, execution_id,
    context_revision, label, object_id, action, message_id, expires_at, consumed_at
FROM telegram_callbacks WHERE part = 0 AND execution_scope = '' AND peer NOT LIKE 'telegram:-%';
DROP TABLE telegram_callbacks;
ALTER TABLE telegram_callbacks_new RENAME TO telegram_callbacks;

-- Preserve the Outbox order and file snapshots. Install the triggers after the copy.
CREATE TABLE outbox_messages_new (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_submission_id TEXT REFERENCES submissions (id),
    kind TEXT NOT NULL CHECK (kind IN ('reply', 'proactive', 'question', 'automation', 'file', 'control', 'feedback')),
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

INSERT INTO outbox_messages_new (rowid, id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at)
SELECT rowid, id, event_id, agent_id, conversation_id, source_submission_id, CASE kind WHEN 'system_error' THEN 'control' ELSE kind END, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at
FROM outbox_messages;
INSERT INTO outbox_files_new (rowid, id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body)
SELECT rowid, id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body FROM outbox_files;

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
        answer = CASE WHEN NEW.kind = 'feedback' THEN '' ELSE answer END
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
-- Restore the old schema. Do not undo retirement or replay settled inputs.
UPDATE automation_runs SET reason = CASE
    WHEN reason = 'the target session is missing or cannot accept Automation inputs'
        THEN 'the target session is missing or cannot accept routine inputs'
    ELSE 'the environment ' || substr(reason, length('the Runner ') + 1) END
WHERE status = 'skipped' AND (reason = 'the target session is missing or cannot accept Automation inputs'
    OR reason GLOB 'the Runner * is unavailable' OR reason GLOB 'the Runner * was removed');

DROP INDEX conversations_native;
ALTER TABLE conversations ADD COLUMN execution_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN scope_context TEXT NOT NULL DEFAULT '{}';
UPDATE conversations SET execution_scope = 'retired-telegram-legacy'
WHERE channel_key LIKE 'retired-telegram:%';
CREATE UNIQUE INDEX conversations_native_scope ON conversations (agent_id, execution_scope, execution_id)
    WHERE execution_id IS NOT NULL;

CREATE INDEX task_entries_task_seq ON task_entries(task_id, seq);
-- Keep dependent rows, foreign keys, and rowids when replacing the checks.
ALTER TABLE submissions ADD COLUMN exit_action_new TEXT NOT NULL DEFAULT ''
    CHECK (exit_action_new IN ('', 'notify_user', 'report_parent', 'stay_silent', 'ask_user'));
UPDATE submissions SET exit_action_new = exit_action;
ALTER TABLE submissions DROP COLUMN exit_action;
ALTER TABLE submissions RENAME COLUMN exit_action_new TO exit_action;

DROP INDEX task_execution_lease;
ALTER TABLE task_executions ADD COLUMN state_new TEXT NOT NULL DEFAULT 'running'
    CHECK (state_new IN ('dispatched', 'running', 'idle', 'completed', 'failed', 'stale', 'cancelled'));
UPDATE task_executions SET state_new = state;
ALTER TABLE task_executions DROP COLUMN state;
ALTER TABLE task_executions RENAME COLUMN state_new TO state;
CREATE INDEX task_execution_lease ON task_executions(lease_expires_at)
    WHERE kind = 'external_agent' AND state = 'running';

DROP TRIGGER release_unanswered_outbox_files;
DROP TRIGGER cancel_unpaired_channel_work;
DROP VIEW outbox_delivery_diagnostics;
ALTER TABLE submissions ADD COLUMN channel_delivery_error TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_commands ADD COLUMN rendered_reply TEXT NOT NULL DEFAULT '';
CREATE TABLE telegram_callbacks_new (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    part INTEGER NOT NULL,
    ordinal INTEGER NOT NULL,
    peer TEXT NOT NULL,
    channel_context TEXT NOT NULL,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    execution_scope TEXT NOT NULL,
    execution_id INTEGER NOT NULL,
    context_revision TEXT NOT NULL,
    label TEXT NOT NULL,
    object_id TEXT NOT NULL,
    action TEXT NOT NULL,
    message_id TEXT NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    UNIQUE(source_id, part, ordinal)
) STRICT;
INSERT INTO telegram_callbacks_new (id, agent_id, source_id, ordinal, peer, channel_context, conversation_id, execution_id,
    context_revision, label, object_id, action, message_id, expires_at, consumed_at, part, execution_scope)
SELECT id, agent_id, source_id, ordinal, peer, channel_context, conversation_id, execution_id,
    context_revision, label, object_id, action, message_id, expires_at, consumed_at, 0, ''
FROM telegram_callbacks;
DROP TABLE telegram_callbacks;
ALTER TABLE telegram_callbacks_new RENAME TO telegram_callbacks;

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

INSERT INTO outbox_messages_new (rowid, id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at)
SELECT rowid, id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, topic_key, body, reason, rendered_message, remote_ids, status, retry_at, attempts, error, created_at, delivered_at
FROM outbox_messages;
INSERT INTO outbox_files_new (rowid, id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body)
SELECT rowid, id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body FROM outbox_files;

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

-- +goose StatementBegin
CREATE TRIGGER revoke_outbox_on_channel_result_revocation AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.kind = 'task_result' AND OLD.channel_revoked = 0 AND NEW.channel_revoked <> 0
BEGIN
  UPDATE outbox_messages SET status = 'failed', error = NEW.channel_delivery_error, retry_at = 0
  WHERE source_submission_id = NEW.id AND status = 'pending';
END;
-- +goose StatementEnd
