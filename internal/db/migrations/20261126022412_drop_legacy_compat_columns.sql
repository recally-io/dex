-- +goose Up
DROP INDEX submissions_channel_replies;
ALTER TABLE submissions DROP COLUMN delivered_parts;
ALTER TABLE submissions DROP COLUMN rendered_reply;
ALTER TABLE submissions DROP COLUMN channel_retry_at;
ALTER TABLE submissions DROP COLUMN channel_remote_ids;
ALTER TABLE submissions DROP COLUMN channel_group_fallback;
ALTER TABLE submissions DROP COLUMN channel_group_cursor;

-- Rebuild the append-only ledger to admit unknown authors without relaxing its guards.
DROP TRIGGER task_entries_no_delete;
DROP TRIGGER task_entries_no_update;
ALTER TABLE task_entries RENAME TO task_entries_previous;
CREATE TABLE task_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'note' CHECK (kind IN ('note', 'artifact', 'status', 'assign', 'review', 'execution', 'external', 'tombstone')),
  tag TEXT NOT NULL DEFAULT '' CHECK (tag IN ('', 'brief', 'decision', 'handoff', 'update')),
  actor_kind TEXT NOT NULL DEFAULT 'owner' CHECK (actor_kind IN ('owner', 'bot', 'external_agent', 'system', 'external', 'unknown')),
  actor_bot_id TEXT NOT NULL DEFAULT '',
  actor_execution_id TEXT NOT NULL DEFAULT '',
  trust TEXT NOT NULL DEFAULT 'owner' CHECK (trust IN ('owner', 'agent', 'external')),
  event_id TEXT REFERENCES task_source_events(id) ON DELETE SET NULL,
  meta_json TEXT NOT NULL DEFAULT '{}',
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(task_id, seq)
) STRICT;
INSERT INTO task_entries (
  id, task_id, seq, kind, tag, actor_kind, actor_bot_id, actor_execution_id, trust, event_id, meta_json, content, created_at
)
SELECT id, task_id, seq, kind, tag,
  CASE WHEN meta_json = '{"legacyAuthor":"unknown"}' THEN 'unknown' ELSE actor_kind END,
  actor_bot_id, actor_execution_id, trust, event_id,
  CASE WHEN meta_json = '{"legacyAuthor":"unknown"}' THEN '{}' ELSE meta_json END,
  content, created_at
FROM task_entries_previous;
DROP TABLE task_entries_previous;
CREATE INDEX task_entries_task_seq ON task_entries(task_id, seq);

-- +goose StatementBegin
CREATE TRIGGER task_entries_no_delete BEFORE DELETE ON task_entries
BEGIN SELECT RAISE(ABORT, 'retract a Task entry with a tombstone'); END;
-- +goose StatementEnd
-- The source-event foreign key may clear event_id; it does not change provenance.
-- +goose StatementBegin
CREATE TRIGGER task_entries_no_update BEFORE UPDATE ON task_entries
WHEN NEW.id IS NOT OLD.id OR NEW.task_id IS NOT OLD.task_id OR NEW.seq IS NOT OLD.seq
  OR NEW.kind IS NOT OLD.kind OR NEW.tag IS NOT OLD.tag OR NEW.actor_kind IS NOT OLD.actor_kind
  OR NEW.actor_bot_id IS NOT OLD.actor_bot_id OR NEW.actor_execution_id IS NOT OLD.actor_execution_id
  OR NEW.trust IS NOT OLD.trust OR NEW.meta_json IS NOT OLD.meta_json OR NEW.content IS NOT OLD.content
  OR NEW.created_at IS NOT OLD.created_at OR (NEW.event_id IS NOT OLD.event_id AND NEW.event_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'Task entries cannot be changed'); END;
-- +goose StatementEnd

-- +goose Down
-- Restore the column definitions. Discarded delivery progress cannot be restored.
ALTER TABLE submissions ADD COLUMN delivered_parts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE submissions ADD COLUMN rendered_reply TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN channel_retry_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE submissions ADD COLUMN channel_remote_ids TEXT NOT NULL DEFAULT '[]';
ALTER TABLE submissions ADD COLUMN channel_group_fallback INTEGER NOT NULL DEFAULT 0 CHECK (channel_group_fallback IN (0, 1));
ALTER TABLE submissions ADD COLUMN channel_group_cursor INTEGER NOT NULL DEFAULT 0 CHECK (channel_group_cursor >= 0);
CREATE INDEX submissions_channel_replies ON submissions (conversation_id, origin)
WHERE status != 'pending' AND delivered_at IS NULL
AND (kind = 'message' OR (kind = 'task_result' AND status = 'done'));

DROP TRIGGER task_entries_no_delete;
DROP TRIGGER task_entries_no_update;
ALTER TABLE task_entries RENAME TO task_entries_previous;
CREATE TABLE task_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'note' CHECK (kind IN ('note', 'artifact', 'status', 'assign', 'review', 'execution', 'external', 'tombstone')),
  tag TEXT NOT NULL DEFAULT '' CHECK (tag IN ('', 'brief', 'decision', 'handoff', 'update')),
  actor_kind TEXT NOT NULL DEFAULT 'owner' CHECK (actor_kind IN ('owner', 'bot', 'external_agent', 'system', 'external')),
  actor_bot_id TEXT NOT NULL DEFAULT '',
  actor_execution_id TEXT NOT NULL DEFAULT '',
  trust TEXT NOT NULL DEFAULT 'owner' CHECK (trust IN ('owner', 'agent', 'external')),
  event_id TEXT REFERENCES task_source_events(id) ON DELETE SET NULL,
  meta_json TEXT NOT NULL DEFAULT '{}',
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(task_id, seq)
) STRICT;
INSERT INTO task_entries (
  id, task_id, seq, kind, tag, actor_kind, actor_bot_id, actor_execution_id, trust, event_id, meta_json, content, created_at
)
SELECT id, task_id, seq, kind, tag,
  CASE WHEN actor_kind = 'unknown' THEN 'owner' ELSE actor_kind END,
  actor_bot_id, actor_execution_id, trust, event_id,
  CASE WHEN actor_kind = 'unknown' THEN '{"legacyAuthor":"unknown"}' ELSE meta_json END,
  content, created_at
FROM task_entries_previous;
DROP TABLE task_entries_previous;
CREATE INDEX task_entries_task_seq ON task_entries(task_id, seq);

-- +goose StatementBegin
CREATE TRIGGER task_entries_no_delete BEFORE DELETE ON task_entries
BEGIN SELECT RAISE(ABORT, 'retract a Task entry with a tombstone'); END;
-- +goose StatementEnd
-- The source-event foreign key may clear event_id; it does not change provenance.
-- +goose StatementBegin
CREATE TRIGGER task_entries_no_update BEFORE UPDATE ON task_entries
WHEN NEW.id IS NOT OLD.id OR NEW.task_id IS NOT OLD.task_id OR NEW.seq IS NOT OLD.seq
  OR NEW.kind IS NOT OLD.kind OR NEW.tag IS NOT OLD.tag OR NEW.actor_kind IS NOT OLD.actor_kind
  OR NEW.actor_bot_id IS NOT OLD.actor_bot_id OR NEW.actor_execution_id IS NOT OLD.actor_execution_id
  OR NEW.trust IS NOT OLD.trust OR NEW.meta_json IS NOT OLD.meta_json OR NEW.content IS NOT OLD.content
  OR NEW.created_at IS NOT OLD.created_at OR (NEW.event_id IS NOT OLD.event_id AND NEW.event_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'Task entries cannot be changed'); END;
-- +goose StatementEnd
