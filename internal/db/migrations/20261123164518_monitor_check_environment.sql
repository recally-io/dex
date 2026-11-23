-- +goose Up
ALTER TABLE monitors ADD COLUMN check_env TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(check_env) AND json_type(check_env) = 'object');

-- +goose Down
ALTER TABLE monitors DROP COLUMN check_env;
