-- +goose Up
ALTER TABLE tasks ADD COLUMN modifies_files INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE tasks DROP COLUMN modifies_files;
