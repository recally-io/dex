-- +goose Up
CREATE TABLE bot_defaults (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    model_provider TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    thinking_level TEXT NOT NULL DEFAULT 'off',
    fallback_provider TEXT NOT NULL DEFAULT '',
    fallback_model_id TEXT NOT NULL DEFAULT '',
    environment_id TEXT NOT NULL DEFAULT ''
) STRICT;

ALTER TABLE agents ADD COLUMN fallback_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN fallback_model_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agents DROP COLUMN fallback_model_id;
ALTER TABLE agents DROP COLUMN fallback_provider;
DROP TABLE bot_defaults;
