-- +goose Up
DROP TRIGGER work_task_entries_no_delete;
DROP TRIGGER work_task_entries_no_update;

ALTER TABLE work_tasks RENAME TO tasks;
ALTER TABLE work_task_numbers RENAME TO task_numbers;
ALTER TABLE work_task_entries RENAME TO task_entries;
ALTER TABLE work_task_inputs RENAME TO task_inputs;
ALTER TABLE work_task_objects RENAME TO task_objects;
ALTER TABLE work_task_waits RENAME TO task_waits;
ALTER TABLE work_task_tool_operations RENAME TO task_tool_operations;
ALTER TABLE work_source_events RENAME TO task_source_events;
ALTER TABLE work_source_receipts RENAME TO task_source_receipts;
ALTER TABLE work_source_holds RENAME TO task_source_holds;

-- SQLite updates index table references but keeps the old index names.
DROP INDEX work_tasks_activity;
DROP INDEX work_tasks_number;
DROP INDEX work_tasks_assignee_status;
DROP INDEX work_tasks_parent;
DROP INDEX work_task_entries_task_seq;
DROP INDEX work_task_waits_open;
DROP INDEX work_source_holds_open;
CREATE INDEX tasks_activity ON tasks(updated_at DESC, id DESC);
CREATE UNIQUE INDEX tasks_number ON tasks(number);
CREATE INDEX tasks_assignee_status ON tasks(assignee_bot_id, status, updated_at DESC);
CREATE INDEX tasks_parent ON tasks(parent_task_id);
CREATE INDEX task_entries_task_seq ON task_entries(task_id, seq);
CREATE INDEX task_waits_open ON task_waits(task_id, namespace, object_type, object_key, event_type)
WHERE resolved_by IS NULL;
CREATE INDEX task_source_holds_open ON task_source_holds(held_at) WHERE decision IS NULL;

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
DROP TRIGGER task_entries_no_delete;
DROP TRIGGER task_entries_no_update;

ALTER TABLE tasks RENAME TO work_tasks;
ALTER TABLE task_numbers RENAME TO work_task_numbers;
ALTER TABLE task_entries RENAME TO work_task_entries;
ALTER TABLE task_inputs RENAME TO work_task_inputs;
ALTER TABLE task_objects RENAME TO work_task_objects;
ALTER TABLE task_waits RENAME TO work_task_waits;
ALTER TABLE task_tool_operations RENAME TO work_task_tool_operations;
ALTER TABLE task_source_events RENAME TO work_source_events;
ALTER TABLE task_source_receipts RENAME TO work_source_receipts;
ALTER TABLE task_source_holds RENAME TO work_source_holds;

DROP INDEX tasks_activity;
DROP INDEX tasks_number;
DROP INDEX tasks_assignee_status;
DROP INDEX tasks_parent;
DROP INDEX task_entries_task_seq;
DROP INDEX task_waits_open;
DROP INDEX task_source_holds_open;
CREATE INDEX work_tasks_activity ON work_tasks(updated_at DESC, id DESC);
CREATE UNIQUE INDEX work_tasks_number ON work_tasks(number);
CREATE INDEX work_tasks_assignee_status ON work_tasks(assignee_bot_id, status, updated_at DESC);
CREATE INDEX work_tasks_parent ON work_tasks(parent_task_id);
CREATE INDEX work_task_entries_task_seq ON work_task_entries(task_id, seq);
CREATE INDEX work_task_waits_open ON work_task_waits(task_id, namespace, object_type, object_key, event_type)
WHERE resolved_by IS NULL;
CREATE INDEX work_source_holds_open ON work_source_holds(held_at) WHERE decision IS NULL;

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
