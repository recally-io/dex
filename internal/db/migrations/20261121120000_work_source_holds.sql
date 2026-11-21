-- +goose Up
-- A held event waits for the owner. The owner attaches it to a Task or dismisses it.
-- An attached event stores its Task in work_source_events.task_id.
CREATE TABLE work_source_holds (
  event_id TEXT PRIMARY KEY REFERENCES work_source_events(id) ON DELETE CASCADE,
  held_at INTEGER NOT NULL,
  decision TEXT CHECK (decision IN ('attached', 'dismissed')),
  decided_at INTEGER,
  CHECK ((decision IS NULL) = (decided_at IS NULL))
) STRICT;
CREATE INDEX work_source_holds_open ON work_source_holds(held_at) WHERE decision IS NULL;

-- +goose Down
DROP TABLE work_source_holds;
