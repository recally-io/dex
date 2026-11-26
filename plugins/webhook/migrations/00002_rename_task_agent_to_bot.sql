-- +goose Up
-- On a new install, version 1 still refers to agents. On upgrade, SQLite has
-- already changed that reference to bots. Rebuild the table for both cases.
CREATE TABLE plugin_webhook_sources_new (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_bot_id TEXT REFERENCES bots(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
INSERT INTO plugin_webhook_sources_new SELECT * FROM plugin_webhook_sources;
DROP TABLE plugin_webhook_sources;
ALTER TABLE plugin_webhook_sources_new RENAME TO plugin_webhook_sources;

-- +goose Down
-- The core downgrade changes the reference from bots back to agents.
CREATE TABLE plugin_webhook_sources_new (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_agent_id TEXT REFERENCES bots(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
INSERT INTO plugin_webhook_sources_new SELECT * FROM plugin_webhook_sources;
DROP TABLE plugin_webhook_sources;
ALTER TABLE plugin_webhook_sources_new RENAME TO plugin_webhook_sources;
