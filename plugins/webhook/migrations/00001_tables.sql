-- +goose Up
-- Before plugin migrations, a core migration made this table without the plugin_ prefix. Make it
-- only when it does not exist, then rename it, so an old database keeps its rows.
-- A signed Webhook source sends normalized Task events. Its ID is also its event namespace.
CREATE TABLE IF NOT EXISTS webhook_sources (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_agent_id TEXT REFERENCES agents(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
ALTER TABLE webhook_sources RENAME TO plugin_webhook_sources;

-- +goose Down
DROP TABLE plugin_webhook_sources;
