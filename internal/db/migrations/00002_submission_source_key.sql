-- +goose Up
-- The message ID on the source channel, for example "telegram:<bot id>:<update id>". A channel can deliver a
-- message more than once; the unique index makes the second delivery a no-op. NULL for channels
-- without redelivery, such as the web UI. SQLite treats NULL values as distinct.
ALTER TABLE submissions ADD COLUMN source_key TEXT;
CREATE UNIQUE INDEX submissions_source_key ON submissions (source_key);

-- +goose Down
DROP INDEX submissions_source_key;
ALTER TABLE submissions DROP COLUMN source_key;
