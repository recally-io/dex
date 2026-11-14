-- +goose Up
ALTER TABLE artifacts ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1));

-- +goose Down
ALTER TABLE artifacts DROP COLUMN deleted;
