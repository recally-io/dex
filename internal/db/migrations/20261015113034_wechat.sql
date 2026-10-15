-- +goose Up
CREATE TABLE wechat_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT NOT NULL,
    base_url TEXT NOT NULL,
    bot_id TEXT NOT NULL,
    owner TEXT NOT NULL
) STRICT;

-- Runtime tokens never enter the settings API. Scope them to both bot and owner.
CREATE TABLE wechat_runtime (
    bot_id TEXT NOT NULL,
    owner TEXT NOT NULL,
    cursor TEXT NOT NULL DEFAULT '',
    context_token TEXT NOT NULL DEFAULT '',
    context_message_id INTEGER NOT NULL DEFAULT 0,
    pause_until INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bot_id, owner)
) STRICT;

-- +goose Down
DROP TABLE wechat_runtime;
DROP TABLE wechat_settings;
