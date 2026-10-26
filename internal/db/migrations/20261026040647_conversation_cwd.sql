-- +goose Up
ALTER TABLE conversations ADD COLUMN cwd TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE conversations DROP COLUMN cwd;
