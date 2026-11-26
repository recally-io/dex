-- +goose Up
-- On a new install, version 1 still refers to agents. On upgrade, SQLite has
-- already changed that reference to bots. Rebuild the table for both cases.
CREATE TABLE plugin_github_webhook_settings_new (
  repository_id INTEGER PRIMARY KEY CHECK (repository_id > 0),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  issue_bot_id TEXT REFERENCES bots(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
INSERT INTO plugin_github_webhook_settings_new SELECT * FROM plugin_github_webhook_settings;
DROP TABLE plugin_github_webhook_settings;
ALTER TABLE plugin_github_webhook_settings_new RENAME TO plugin_github_webhook_settings;

-- +goose Down
-- The core downgrade changes the reference from bots back to agents.
CREATE TABLE plugin_github_webhook_settings_new (
  repository_id INTEGER PRIMARY KEY CHECK (repository_id > 0),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  issue_agent_id TEXT REFERENCES bots(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
INSERT INTO plugin_github_webhook_settings_new SELECT * FROM plugin_github_webhook_settings;
DROP TABLE plugin_github_webhook_settings;
ALTER TABLE plugin_github_webhook_settings_new RENAME TO plugin_github_webhook_settings;
