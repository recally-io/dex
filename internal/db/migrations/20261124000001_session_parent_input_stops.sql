-- +goose Up
ALTER TABLE submissions ADD COLUMN stop_requested INTEGER NOT NULL DEFAULT 0 CHECK (stop_requested IN (0, 1));

-- +goose Down
ALTER TABLE submissions DROP COLUMN stop_requested;
