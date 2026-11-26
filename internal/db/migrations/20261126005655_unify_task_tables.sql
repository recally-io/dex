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
