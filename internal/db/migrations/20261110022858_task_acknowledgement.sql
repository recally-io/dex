-- +goose Up
ALTER TABLE tasks ADD COLUMN handled_at INTEGER;

-- +goose Down
ALTER TABLE tasks DROP COLUMN handled_at;
