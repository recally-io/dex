-- +goose Up
CREATE TABLE feishu_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    app_id TEXT NOT NULL,
    app_secret TEXT NOT NULL,
    brand TEXT NOT NULL CHECK (brand IN ('feishu', 'lark')),
    owner_id TEXT NOT NULL DEFAULT '',
    pairing_code TEXT NOT NULL DEFAULT '',
    pairing_expires_at INTEGER NOT NULL DEFAULT 0
) STRICT;

-- +goose Down
DROP TABLE feishu_settings;
