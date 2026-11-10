-- +goose Up
ALTER TABLE fork_operations ADD COLUMN tool_source_request_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE fork_operations DROP COLUMN tool_source_request_id;
