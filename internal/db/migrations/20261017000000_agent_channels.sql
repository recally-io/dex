-- +goose Up
CREATE TABLE telegram_settings_agent (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id),
    token TEXT NOT NULL UNIQUE,
    allowed_users TEXT NOT NULL,
    pairing_code TEXT NOT NULL DEFAULT '',
    pairing_expires INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO telegram_settings_agent (agent_id, token, allowed_users, pairing_code, pairing_expires) SELECT 'default', token, allowed_users, pairing_code, pairing_expires FROM telegram_settings;
DROP TABLE telegram_settings;
ALTER TABLE telegram_settings_agent RENAME TO telegram_settings;

CREATE TABLE wechat_settings_agent (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id),
    token TEXT NOT NULL,
    base_url TEXT NOT NULL,
    bot_id TEXT NOT NULL UNIQUE,
    owner TEXT NOT NULL
) STRICT;
INSERT INTO wechat_settings_agent (agent_id, token, base_url, bot_id, owner) SELECT 'default', token, base_url, bot_id, owner FROM wechat_settings;
DROP TABLE wechat_settings;
ALTER TABLE wechat_settings_agent RENAME TO wechat_settings;

CREATE TABLE feishu_settings_agent (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id),
    app_id TEXT NOT NULL UNIQUE,
    app_secret TEXT NOT NULL,
    brand TEXT NOT NULL CHECK (brand IN ('feishu', 'lark')),
    owner_id TEXT NOT NULL DEFAULT '',
    pairing_code TEXT NOT NULL DEFAULT '',
    pairing_expires_at INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO feishu_settings_agent (agent_id, app_id, app_secret, brand, owner_id, pairing_code, pairing_expires_at) SELECT 'default', app_id, app_secret, brand, owner_id, pairing_code, pairing_expires_at FROM feishu_settings;
DROP TABLE feishu_settings;
ALTER TABLE feishu_settings_agent RENAME TO feishu_settings;

CREATE TABLE channel_commands_agent (
    agent_id TEXT NOT NULL REFERENCES agents(id),
    source_key TEXT PRIMARY KEY,
    peer TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    command TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    completed INTEGER NOT NULL DEFAULT 0,
    delivered_at INTEGER
) STRICT;
INSERT INTO channel_commands_agent (agent_id, source_key, peer, channel_key, command, answer, completed, delivered_at) SELECT 'default', source_key, peer, channel_key, command, answer, completed, delivered_at FROM channel_commands;
DROP TABLE channel_commands;
ALTER TABLE channel_commands_agent RENAME TO channel_commands;

CREATE INDEX channel_command_delivery ON channel_commands (agent_id, completed, delivered_at);

-- +goose Down
CREATE TABLE channel_commands_global (
    source_key TEXT PRIMARY KEY,
    peer TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    command TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    completed INTEGER NOT NULL DEFAULT 0,
    delivered_at INTEGER
) STRICT;
INSERT INTO channel_commands_global (source_key, peer, channel_key, command, answer, completed, delivered_at) SELECT source_key, peer, channel_key, command, answer, completed, delivered_at FROM channel_commands WHERE agent_id = 'default';
DROP TABLE channel_commands;
ALTER TABLE channel_commands_global RENAME TO channel_commands;

CREATE TABLE feishu_settings_global (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    app_id TEXT NOT NULL,
    app_secret TEXT NOT NULL,
    brand TEXT NOT NULL CHECK (brand IN ('feishu', 'lark')),
    owner_id TEXT NOT NULL DEFAULT '',
    pairing_code TEXT NOT NULL DEFAULT '',
    pairing_expires_at INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO feishu_settings_global (id, app_id, app_secret, brand, owner_id, pairing_code, pairing_expires_at) SELECT 1, app_id, app_secret, brand, owner_id, pairing_code, pairing_expires_at FROM feishu_settings WHERE agent_id = 'default';
DROP TABLE feishu_settings;
ALTER TABLE feishu_settings_global RENAME TO feishu_settings;

CREATE TABLE wechat_settings_global (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT NOT NULL,
    base_url TEXT NOT NULL,
    bot_id TEXT NOT NULL,
    owner TEXT NOT NULL
) STRICT;
INSERT INTO wechat_settings_global (id, token, base_url, bot_id, owner) SELECT 1, token, base_url, bot_id, owner FROM wechat_settings WHERE agent_id = 'default';
DROP TABLE wechat_settings;
ALTER TABLE wechat_settings_global RENAME TO wechat_settings;

CREATE TABLE telegram_settings_global (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT NOT NULL,
    allowed_users TEXT NOT NULL,
    pairing_code TEXT NOT NULL DEFAULT '',
    pairing_expires INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO telegram_settings_global (id, token, allowed_users, pairing_code, pairing_expires) SELECT 1, token, allowed_users, pairing_code, pairing_expires FROM telegram_settings WHERE agent_id = 'default';
DROP TABLE telegram_settings;
ALTER TABLE telegram_settings_global RENAME TO telegram_settings;
