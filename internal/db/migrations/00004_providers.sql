-- +goose Up
-- One row per provider that the user added: a pi-ai built-in provider (api = '') with its API key,
-- or a custom provider with its own API, base URL, and models.
CREATE TABLE providers (
    -- The pi-ai provider ID. The user chooses it for a custom provider.
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    api TEXT NOT NULL CHECK (
        api IN ('', 'openai-completions', 'openai-responses', 'anthropic-messages')
    ),
    base_url TEXT NOT NULL DEFAULT '',
    api_key TEXT NOT NULL DEFAULT '',
    -- Model IDs separated by commas. Empty: list the models from the endpoint.
    models TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
) STRICT;

INSERT INTO providers (id, name, api, base_url, api_key, created_at)
SELECT
    provider_id,
    CASE WHEN provider_id = 'openai-compatible' THEN 'OpenAI-compatible gateway' ELSE provider_id END,
    CASE WHEN provider_id = 'openai-compatible' THEN 'openai-completions' ELSE '' END,
    base_url,
    api_key,
    CAST(unixepoch('subsec') * 1000 AS INTEGER)
FROM provider_settings;

DROP TABLE provider_settings;

-- +goose Down
CREATE TABLE provider_settings (
    provider_id TEXT PRIMARY KEY,
    api_key TEXT NOT NULL,
    base_url TEXT NOT NULL DEFAULT ''
) STRICT;

INSERT INTO provider_settings (provider_id, api_key, base_url)
SELECT id, api_key, base_url FROM providers WHERE api IN ('', 'openai-completions');

DROP TABLE providers;
