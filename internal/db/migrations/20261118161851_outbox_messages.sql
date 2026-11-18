-- +goose Up
ALTER TABLE submissions ADD COLUMN exit_action TEXT NOT NULL DEFAULT ''
    CHECK (exit_action IN ('', 'notify_user', 'report_parent', 'stay_silent', 'ask_user'));

CREATE TABLE outbox_messages (
    id TEXT PRIMARY KEY,
    dedup_key TEXT NOT NULL UNIQUE,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_submission_id TEXT REFERENCES submissions (id),
    kind TEXT NOT NULL CHECK (kind IN ('reply', 'proactive', 'question', 'routine', 'system_error')),
    peer TEXT NOT NULL DEFAULT '',
    channel_context TEXT NOT NULL DEFAULT '{}',
    topic_key TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    rendered_parts TEXT NOT NULL DEFAULT '',
    delivered_parts INTEGER NOT NULL DEFAULT 0 CHECK (delivered_parts >= 0),
    group_fallback INTEGER NOT NULL DEFAULT 0 CHECK (group_fallback IN (0, 1)),
    group_cursor INTEGER NOT NULL DEFAULT 0 CHECK (group_cursor >= 0),
    remote_ids TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'superseded', 'revoked', 'failed')),
    retry_at INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    read_at INTEGER,
    dismissed_at INTEGER,
    created_at INTEGER NOT NULL,
    delivered_at INTEGER
) STRICT;

CREATE INDEX outbox_messages_pending ON outbox_messages (agent_id, peer) WHERE status = 'pending';
CREATE INDEX outbox_messages_conv_delivered ON outbox_messages (conversation_id, delivered_at DESC) WHERE status = 'delivered';
CREATE INDEX outbox_messages_topic ON outbox_messages (conversation_id, peer, topic_key) WHERE status = 'pending' AND topic_key != '';
CREATE INDEX outbox_messages_source ON outbox_messages (source_submission_id) WHERE source_submission_id IS NOT NULL;

CREATE TABLE outbox_files (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    operation_key TEXT NOT NULL,
    submission_id TEXT REFERENCES submissions (id) ON DELETE CASCADE,
    outbox_id TEXT REFERENCES outbox_messages (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    body BLOB NOT NULL CHECK (length(body) <= 20971520),
    UNIQUE (agent_id, operation_key)
) STRICT;

CREATE INDEX outbox_files_submission ON outbox_files (submission_id) WHERE submission_id IS NOT NULL;
CREATE INDEX outbox_files_outbox ON outbox_files (outbox_id) WHERE outbox_id IS NOT NULL;

-- +goose StatementBegin
CREATE TRIGGER release_outbox_files AFTER UPDATE OF status ON outbox_messages
WHEN NEW.status != 'pending'
BEGIN
    DELETE FROM outbox_files WHERE outbox_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER release_unanswered_outbox_files AFTER UPDATE OF status ON submissions
WHEN NEW.status = 'unanswered'
BEGIN
    DELETE FROM outbox_files WHERE submission_id = NEW.id AND outbox_id IS NULL;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER revoke_deleted_telegram_outbox BEFORE DELETE ON telegram_settings
BEGIN
    UPDATE outbox_messages
    SET status = 'revoked', error = 'Telegram authorization revoked'
    WHERE status = 'pending' AND peer LIKE 'telegram:%' AND agent_id = OLD.agent_id;
END;
-- +goose StatementEnd

-- Drain any settled, undelivered channel replies from submissions into outbox_messages.
INSERT INTO outbox_messages (
    id, dedup_key, agent_id, conversation_id, source_submission_id,
    kind, peer, channel_context, topic_key, body, reason,
    rendered_parts, delivered_parts, group_fallback, group_cursor, remote_ids,
    status, retry_at, error, created_at
)
SELECT
    s.id,
    'reply:' || s.id,
    c.agent_id,
    s.conversation_id,
    s.id,
    CASE WHEN EXISTS (SELECT 1 FROM questions q WHERE q.source_submission_id = s.id) THEN 'question' ELSE 'reply' END,
    s.origin,
    s.channel_context,
    '',
    CASE WHEN s.status = 'done' THEN coalesce(s.answer, '') ELSE '' END,
    CASE WHEN s.status != 'done' THEN coalesce(s.reason, '') ELSE '' END,
    s.rendered_reply,
    s.delivered_parts,
    s.channel_group_fallback,
    s.channel_group_cursor,
    s.channel_remote_ids,
    'pending',
    s.channel_retry_at,
    s.channel_delivery_error,
    coalesce(s.settled_at, s.created_at)
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.status != 'pending'
  AND s.delivered_at IS NULL
  AND s.channel_revoked = 0
  AND s.origin LIKE '%:%'
  AND (s.kind = 'message' OR (s.kind = 'task_result' AND s.status = 'done'))
ORDER BY s.rowid
ON CONFLICT DO NOTHING;

INSERT INTO outbox_files (id, agent_id, operation_key, submission_id, outbox_id, name, mime_type, body)
SELECT
    f.operation_key,
    o.agent_id,
    f.operation_key,
    f.submission_id,
    o.id,
    f.name,
    f.mime_type,
    f.body
FROM channel_reply_files f
JOIN outbox_messages o ON o.id = f.submission_id
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TRIGGER IF EXISTS revoke_deleted_telegram_outbox;
DROP TRIGGER IF EXISTS release_unanswered_outbox_files;
DROP TRIGGER IF EXISTS release_outbox_files;
DROP INDEX IF EXISTS outbox_files_outbox;
DROP INDEX IF EXISTS outbox_files_submission;
DROP TABLE IF EXISTS outbox_files;
DROP INDEX IF EXISTS outbox_messages_source;
DROP INDEX IF EXISTS outbox_messages_topic;
DROP INDEX IF EXISTS outbox_messages_conv_delivered;
DROP INDEX IF EXISTS outbox_messages_pending;
DROP TABLE IF EXISTS outbox_messages;
ALTER TABLE submissions DROP COLUMN exit_action;
