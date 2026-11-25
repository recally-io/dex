-- +goose Up
CREATE TABLE work_task_numbers (
  number INTEGER PRIMARY KEY AUTOINCREMENT
) STRICT;

ALTER TABLE work_tasks RENAME COLUMN agent_id TO assignee_bot_id;
ALTER TABLE work_tasks ADD COLUMN number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE work_tasks ADD COLUMN status TEXT NOT NULL DEFAULT 'ready'
  CHECK (status IN ('backlog', 'ready', 'in_progress', 'in_review', 'done', 'cancelled'));
UPDATE work_tasks SET status = CASE lifecycle WHEN 'open' THEN 'in_progress' WHEN 'completed' THEN 'done' ELSE 'cancelled' END;
ALTER TABLE work_tasks DROP COLUMN lifecycle;
ALTER TABLE work_tasks ADD COLUMN parent_task_id TEXT REFERENCES work_tasks(id);
ALTER TABLE work_tasks ADD COLUMN authorized_by_root_id TEXT REFERENCES work_tasks(id);
ALTER TABLE work_tasks ADD COLUMN creator_kind TEXT NOT NULL DEFAULT 'owner' CHECK (creator_kind IN ('owner', 'bot', 'source'));
ALTER TABLE work_tasks ADD COLUMN creator_id TEXT NOT NULL DEFAULT '';
ALTER TABLE work_tasks ADD COLUMN reviewer_kind TEXT NOT NULL DEFAULT '' CHECK (reviewer_kind IN ('', 'owner', 'bot'));
ALTER TABLE work_tasks ADD COLUMN reviewer_id TEXT NOT NULL DEFAULT '';
ALTER TABLE work_tasks ADD COLUMN review_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE work_tasks ADD COLUMN next_entry_seq INTEGER NOT NULL DEFAULT 1;
ALTER TABLE work_tasks ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;

CREATE TEMP TABLE work_task_number_backfill (id TEXT PRIMARY KEY, number INTEGER NOT NULL) STRICT;
INSERT INTO work_task_number_backfill SELECT id, row_number() OVER (ORDER BY created_at, id) FROM work_tasks;
UPDATE work_tasks SET number = (SELECT n.number FROM work_task_number_backfill n WHERE n.id = work_tasks.id);
DROP TABLE work_task_number_backfill;
CREATE UNIQUE INDEX work_tasks_number ON work_tasks(number);
INSERT INTO work_task_numbers(number) SELECT number FROM work_tasks ORDER BY number DESC LIMIT 1;
DELETE FROM work_task_numbers;
CREATE INDEX work_tasks_assignee_status ON work_tasks(assignee_bot_id, status, updated_at DESC);

ALTER TABLE work_task_entries RENAME TO work_task_entries_old;
CREATE TABLE work_task_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL REFERENCES work_tasks(id),
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'note' CHECK (kind IN ('note', 'artifact', 'status', 'assign', 'review', 'execution', 'external', 'tombstone')),
  tag TEXT NOT NULL DEFAULT '' CHECK (tag IN ('', 'brief', 'decision', 'handoff', 'update')),
  actor_kind TEXT NOT NULL DEFAULT 'owner' CHECK (actor_kind IN ('owner', 'bot', 'external_agent', 'system', 'external')),
  actor_bot_id TEXT NOT NULL DEFAULT '',
  actor_execution_id TEXT NOT NULL DEFAULT '',
  trust TEXT NOT NULL DEFAULT 'owner' CHECK (trust IN ('owner', 'agent', 'external')),
  event_id TEXT REFERENCES work_source_events(id) ON DELETE SET NULL,
  meta_json TEXT NOT NULL DEFAULT '{}',
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(task_id, seq)
) STRICT;
CREATE INDEX work_task_entries_task_seq ON work_task_entries(task_id, seq);
INSERT INTO work_task_entries (id, task_id, seq, kind, actor_kind, actor_bot_id, trust, event_id, meta_json, content, created_at)
SELECT e.id, e.task_id, row_number() OVER (PARTITION BY e.task_id ORDER BY e.id),
  CASE WHEN e.event_id IS NOT NULL THEN 'external' ELSE 'note' END,
  CASE WHEN e.event_id IS NOT NULL THEN 'external' WHEN e.author = 'agent' THEN 'bot' ELSE 'owner' END,
  CASE WHEN e.event_id IS NULL AND e.author = 'agent' THEN t.assignee_bot_id ELSE '' END,
  CASE WHEN e.event_id IS NOT NULL THEN 'external' WHEN e.author = 'agent' THEN 'agent' ELSE 'owner' END,
  e.event_id, CASE WHEN e.author = '' THEN '{"legacyAuthor":"unknown"}' ELSE '{}' END,
  e.content, e.created_at
FROM work_task_entries_old e JOIN work_tasks t ON t.id = e.task_id;
UPDATE work_tasks SET next_entry_seq = coalesce((SELECT max(seq) + 1 FROM work_task_entries e WHERE e.task_id = work_tasks.id), 1);
DROP TABLE work_task_entries_old;
-- +goose StatementBegin
CREATE TRIGGER work_task_entries_no_delete BEFORE DELETE ON work_task_entries
BEGIN SELECT RAISE(ABORT, 'retract a Task entry with a tombstone'); END;
-- +goose StatementEnd
-- The source-event foreign key may clear event_id; it does not change provenance.
-- +goose StatementBegin
CREATE TRIGGER work_task_entries_no_update BEFORE UPDATE ON work_task_entries
WHEN NEW.id IS NOT OLD.id OR NEW.task_id IS NOT OLD.task_id OR NEW.seq IS NOT OLD.seq
  OR NEW.kind IS NOT OLD.kind OR NEW.tag IS NOT OLD.tag OR NEW.actor_kind IS NOT OLD.actor_kind
  OR NEW.actor_bot_id IS NOT OLD.actor_bot_id OR NEW.actor_execution_id IS NOT OLD.actor_execution_id
  OR NEW.trust IS NOT OLD.trust OR NEW.meta_json IS NOT OLD.meta_json OR NEW.content IS NOT OLD.content
  OR NEW.created_at IS NOT OLD.created_at OR (NEW.event_id IS NOT OLD.event_id AND NEW.event_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'Task entries cannot be changed'); END;
-- +goose StatementEnd

CREATE TABLE task_executions (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES work_tasks(id),
  bot_id TEXT NOT NULL REFERENCES agents(id),
  kind TEXT NOT NULL CHECK (kind IN ('dex_session', 'external_agent')),
  conversation_id INTEGER REFERENCES conversations(id) ON DELETE CASCADE,
  turn_request_id TEXT NOT NULL DEFAULT '',
  is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
  harness TEXT NOT NULL DEFAULT '',
  machine_id TEXT NOT NULL DEFAULT '',
  host_meta_json TEXT NOT NULL DEFAULT '{}',
  workspace_path TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  external_session_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('dispatched', 'running', 'idle', 'completed', 'failed', 'stale', 'cancelled')),
  lease_epoch INTEGER NOT NULL DEFAULT 0,
  lease_expires_at INTEGER,
  last_heartbeat_at INTEGER,
  started_at INTEGER NOT NULL,
  ended_at INTEGER
) STRICT;
CREATE UNIQUE INDEX task_executions_primary ON task_executions(task_id) WHERE is_primary = 1;
CREATE UNIQUE INDEX task_executions_session ON task_executions(task_id, conversation_id) WHERE conversation_id IS NOT NULL;
CREATE INDEX task_executions_conversation ON task_executions(conversation_id) WHERE conversation_id IS NOT NULL;
CREATE INDEX task_executions_task ON task_executions(task_id, is_primary DESC, started_at, id);
INSERT INTO task_executions (id, task_id, bot_id, kind, conversation_id, is_primary, started_at)
SELECT 'session:' || task_id || ':' || conversation_id, task_id, agent_id, 'dex_session', conversation_id, is_primary, linked_at
FROM work_task_sessions;
DROP TABLE work_task_sessions;

DROP INDEX submissions_conversation_kind;
DROP INDEX submissions_channel_replies;
DROP TRIGGER revoke_outbox_on_channel_result_revocation;
ALTER TABLE submissions RENAME COLUMN kind TO previous_kind;
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
  CHECK (kind IN ('message', 'task_result', 'context_reset', 'task_wake'));
UPDATE submissions SET kind = previous_kind;
ALTER TABLE submissions DROP COLUMN previous_kind;
CREATE INDEX submissions_conversation_kind ON submissions (conversation_id, kind);
CREATE INDEX submissions_channel_replies ON submissions (conversation_id, origin)
WHERE status != 'pending' AND delivered_at IS NULL
AND (kind = 'message' OR (kind = 'task_result' AND status = 'done'));
-- +goose StatementBegin
CREATE TRIGGER revoke_outbox_on_channel_result_revocation AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.kind = 'task_result' AND OLD.channel_revoked = 0 AND NEW.channel_revoked <> 0
BEGIN
  UPDATE outbox_messages SET status = 'failed', error = NEW.channel_delivery_error, retry_at = 0
  WHERE source_submission_id = NEW.id AND status = 'pending';
END;
-- +goose StatementEnd
CREATE UNIQUE INDEX submissions_one_unadmitted_task_wake ON submissions(conversation_id) WHERE kind = 'task_wake' AND admitted_at IS NULL;

-- +goose Down
DROP INDEX submissions_one_unadmitted_task_wake;
DROP INDEX submissions_conversation_kind;
DROP INDEX submissions_channel_replies;
DROP TRIGGER revoke_outbox_on_channel_result_revocation;
ALTER TABLE submissions RENAME COLUMN kind TO previous_kind;
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
  CHECK (kind IN ('message', 'task_result', 'context_reset'));
-- Keep submission IDs and their dependent rows on downgrade.
UPDATE submissions SET kind = CASE previous_kind WHEN 'task_wake' THEN 'task_result' ELSE previous_kind END;
ALTER TABLE submissions DROP COLUMN previous_kind;
CREATE INDEX submissions_conversation_kind ON submissions (conversation_id, kind);
CREATE INDEX submissions_channel_replies ON submissions (conversation_id, origin)
WHERE status != 'pending' AND delivered_at IS NULL
AND (kind = 'message' OR (kind = 'task_result' AND status = 'done'));
-- +goose StatementBegin
CREATE TRIGGER revoke_outbox_on_channel_result_revocation AFTER UPDATE OF channel_revoked ON submissions
WHEN NEW.kind = 'task_result' AND OLD.channel_revoked = 0 AND NEW.channel_revoked <> 0
BEGIN
  UPDATE outbox_messages SET status = 'failed', error = NEW.channel_delivery_error, retry_at = 0
  WHERE source_submission_id = NEW.id AND status = 'pending';
END;
-- +goose StatementEnd

CREATE TABLE work_task_sessions (
  task_id TEXT NOT NULL REFERENCES work_tasks(id) ON DELETE CASCADE,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
  linked_at INTEGER NOT NULL,
  PRIMARY KEY (task_id, conversation_id)
) STRICT;
CREATE UNIQUE INDEX work_task_sessions_primary ON work_task_sessions(task_id) WHERE is_primary = 1;
CREATE INDEX work_task_sessions_conversation ON work_task_sessions(conversation_id);
INSERT INTO work_task_sessions (task_id, conversation_id, agent_id, is_primary, linked_at)
SELECT task_id, conversation_id, bot_id, is_primary, started_at
FROM task_executions WHERE kind = 'dex_session' AND conversation_id IS NOT NULL;
DROP TABLE task_executions;

DROP TRIGGER work_task_entries_no_update;
DROP TRIGGER work_task_entries_no_delete;
ALTER TABLE work_task_entries RENAME TO work_task_entries_board;
CREATE TABLE work_task_entries (
  id INTEGER PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES work_tasks(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  author TEXT NOT NULL DEFAULT '' CHECK (author IN ('', 'owner', 'agent')),
  event_id TEXT REFERENCES work_source_events(id) ON DELETE SET NULL
) STRICT;
CREATE INDEX work_task_entries_task ON work_task_entries(task_id, id);
INSERT INTO work_task_entries(id, task_id, content, created_at, author, event_id)
SELECT id, task_id, content, created_at,
  CASE WHEN json_extract(meta_json, '$.legacyAuthor') = 'unknown' OR trust = 'external' THEN ''
       WHEN trust = 'agent' THEN 'agent' ELSE 'owner' END,
  event_id
FROM work_task_entries_board
WHERE kind IN ('note', 'artifact', 'external') AND json_extract(meta_json, '$.legacyHidden') IS NOT 1 AND NOT EXISTS (
  SELECT 1 FROM work_task_entries_board r
  WHERE r.task_id = work_task_entries_board.task_id AND r.kind = 'tombstone'
    AND json_extract(r.meta_json, '$.targetSeq') = work_task_entries_board.seq
);
DROP TABLE work_task_entries_board;

DROP INDEX work_tasks_number;
DROP INDEX work_tasks_assignee_status;
ALTER TABLE work_tasks ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'open' CHECK (lifecycle IN ('open', 'completed', 'cancelled'));
UPDATE work_tasks SET lifecycle = CASE status WHEN 'done' THEN 'completed' WHEN 'cancelled' THEN 'cancelled' ELSE 'open' END;
ALTER TABLE work_tasks DROP COLUMN revision;
ALTER TABLE work_tasks DROP COLUMN next_entry_seq;
ALTER TABLE work_tasks DROP COLUMN review_seq;
ALTER TABLE work_tasks DROP COLUMN reviewer_id;
ALTER TABLE work_tasks DROP COLUMN reviewer_kind;
ALTER TABLE work_tasks DROP COLUMN creator_id;
ALTER TABLE work_tasks DROP COLUMN creator_kind;
ALTER TABLE work_tasks DROP COLUMN authorized_by_root_id;
ALTER TABLE work_tasks DROP COLUMN parent_task_id;
ALTER TABLE work_tasks DROP COLUMN status;
ALTER TABLE work_tasks DROP COLUMN number;
ALTER TABLE work_tasks RENAME COLUMN assignee_bot_id TO agent_id;
DROP TABLE work_task_numbers;
