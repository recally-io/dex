-- +goose Up
ALTER TABLE work_source_events ADD COLUMN author_login TEXT NOT NULL DEFAULT '';
ALTER TABLE work_source_events ADD COLUMN author_association TEXT NOT NULL DEFAULT '';
ALTER TABLE work_task_entries ADD COLUMN event_id TEXT REFERENCES work_source_events(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE work_task_entries DROP COLUMN event_id;
ALTER TABLE work_source_events DROP COLUMN author_association;
ALTER TABLE work_source_events DROP COLUMN author_login;
