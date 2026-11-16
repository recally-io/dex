-- +goose Up
ALTER TABLE channel_commands ADD COLUMN channel_context TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE channel_commands DROP COLUMN channel_context;
