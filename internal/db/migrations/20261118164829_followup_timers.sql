-- +goose Up
CREATE TABLE followup_timers (
    id TEXT PRIMARY KEY,
    operation_key TEXT NOT NULL UNIQUE,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    root_request_id TEXT NOT NULL REFERENCES submissions (id),
    source_submission_id TEXT NOT NULL REFERENCES submissions (id),
    chain_depth INTEGER NOT NULL CHECK (chain_depth >= 0 AND chain_depth <= 6),
    due_at INTEGER NOT NULL,
    time_zone TEXT NOT NULL DEFAULT 'UTC',
    prompt TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'scheduled' CHECK (state IN ('scheduled', 'fired', 'cancelled', 'expired')),
    fire_submission_id TEXT UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX followup_timers_due ON followup_timers (due_at) WHERE state = 'scheduled';
CREATE INDEX followup_timers_conv ON followup_timers (conversation_id, created_at DESC);

-- +goose StatementBegin
CREATE TRIGGER cancel_archived_agent_timers AFTER UPDATE OF archived_at ON agents
WHEN NEW.archived_at IS NOT NULL
BEGIN
    UPDATE followup_timers
    SET state = 'cancelled', updated_at = NEW.archived_at
    WHERE agent_id = NEW.id AND state = 'scheduled';
END;
-- +goose StatementEnd

-- Drain any pending routine notification deliveries into outbox_messages.
INSERT INTO outbox_messages (
    id, dedup_key, agent_id, conversation_id, source_submission_id,
    kind, peer, channel_context, topic_key, body, reason,
    rendered_parts, delivered_parts, status, created_at
)
SELECT
    'routine:' || d.run_id,
    'routine:' || d.run_id,
    d.agent_id,
    coalesce(
        (SELECT s.conversation_id FROM submissions s WHERE s.id = d.run_id),
        (SELECT c.id FROM conversations c WHERE c.agent_id = d.agent_id ORDER BY c.id LIMIT 1)
    ),
    (SELECT s.id FROM submissions s WHERE s.id = d.run_id),
    'routine',
    d.peer,
    json_object('accountId', d.account_id),
    coalesce((SELECT 'routine:' || r.automation_id FROM automation_runs r WHERE r.id = d.run_id), ''),
    d.body,
    '',
    d.rendered_parts,
    d.parts,
    'pending',
    coalesce((SELECT r.created_at FROM automation_runs r WHERE r.id = d.run_id), 0)
FROM routine_notification_deliveries d
WHERE d.status = 'pending'
  AND coalesce(
      (SELECT s.conversation_id FROM submissions s WHERE s.id = d.run_id),
      (SELECT c.id FROM conversations c WHERE c.agent_id = d.agent_id ORDER BY c.id LIMIT 1)
  ) IS NOT NULL
ORDER BY d.rowid
ON CONFLICT DO NOTHING;

UPDATE routine_notification_deliveries
SET status = 'delivered'
WHERE status = 'pending';

-- +goose Down
DROP TRIGGER IF EXISTS cancel_archived_agent_timers;
DROP INDEX IF EXISTS followup_timers_conv;
DROP INDEX IF EXISTS followup_timers_due;
DROP TABLE IF EXISTS followup_timers;
