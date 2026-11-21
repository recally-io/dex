-- +goose Up
CREATE TABLE work_task_objects (
  namespace TEXT NOT NULL,
  object_type TEXT NOT NULL,
  object_key TEXT NOT NULL,
  task_id TEXT NOT NULL REFERENCES work_tasks(id) ON DELETE CASCADE,
  PRIMARY KEY (namespace, object_type, object_key)
) STRICT;

CREATE TABLE work_source_events (
  id TEXT PRIMARY KEY,
  namespace TEXT NOT NULL,
  change_key TEXT,
  event_type TEXT NOT NULL,
  objects TEXT NOT NULL,
  revision TEXT NOT NULL,
  content TEXT NOT NULL,
  source_url TEXT NOT NULL,
  occurred_at INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  task_id TEXT REFERENCES work_tasks(id) ON DELETE SET NULL,
  disposition TEXT NOT NULL CHECK (disposition IN ('attached', 'unmatched', 'conflict')),
  UNIQUE (namespace, change_key)
) STRICT;

CREATE TABLE work_source_receipts (
  namespace TEXT NOT NULL,
  receipt_key TEXT NOT NULL,
  event_id TEXT NOT NULL REFERENCES work_source_events(id) DEFERRABLE INITIALLY DEFERRED,
  PRIMARY KEY (namespace, receipt_key)
) STRICT;

CREATE TABLE work_task_waits (
  id INTEGER PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES work_tasks(id) ON DELETE CASCADE,
  namespace TEXT NOT NULL,
  object_type TEXT NOT NULL,
  object_key TEXT NOT NULL,
  event_type TEXT NOT NULL,
  revision TEXT NOT NULL,
  after_at INTEGER NOT NULL,
  resolved_by TEXT REFERENCES work_source_events(id),
  UNIQUE (task_id, namespace, object_type, object_key, event_type, revision, after_at)
) STRICT;
CREATE INDEX work_task_waits_open ON work_task_waits(task_id, namespace, object_type, object_key, event_type)
WHERE resolved_by IS NULL;

-- +goose Down
DROP TABLE work_task_waits;
DROP TABLE work_source_receipts;
DROP TABLE work_source_events;
DROP TABLE work_task_objects;
