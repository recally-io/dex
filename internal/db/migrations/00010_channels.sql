-- +goose Up
CREATE TABLE channel_peers (
    peer TEXT PRIMARY KEY,
    channel_key TEXT NOT NULL
) STRICT;

-- Commands keep their outcome, so a repeated /new cannot rotate twice.
CREATE TABLE channel_commands (
    source_key TEXT PRIMARY KEY,
    peer TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    command TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    completed INTEGER NOT NULL DEFAULT 0,
    delivered_parts INTEGER NOT NULL DEFAULT 0,
    delivered_at INTEGER
) STRICT;

ALTER TABLE submissions ADD COLUMN delivered_at INTEGER;
ALTER TABLE submissions ADD COLUMN delivered_parts INTEGER NOT NULL DEFAULT 0;

-- Existing replies predate delivery tracking. Do not send the whole history.
UPDATE submissions SET delivered_at = settled_at WHERE status != 'pending';

-- +goose Down
ALTER TABLE submissions DROP COLUMN delivered_parts;
ALTER TABLE submissions DROP COLUMN delivered_at;
DROP TABLE channel_commands;
DROP TABLE channel_peers;
