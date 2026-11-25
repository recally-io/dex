-- +goose Up
ALTER TABLE execution_environments ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE execution_environments DROP COLUMN description;
