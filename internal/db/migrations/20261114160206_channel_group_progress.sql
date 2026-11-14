-- +goose Up
-- A rejected group uses single-file delivery. Confirmed members must not repeat after restart.
ALTER TABLE submissions ADD COLUMN channel_group_fallback INTEGER NOT NULL DEFAULT 0 CHECK (channel_group_fallback IN (0, 1));
ALTER TABLE submissions ADD COLUMN channel_group_cursor INTEGER NOT NULL DEFAULT 0 CHECK (channel_group_cursor >= 0);

-- +goose Down
ALTER TABLE submissions DROP COLUMN channel_group_cursor;
ALTER TABLE submissions DROP COLUMN channel_group_fallback;
