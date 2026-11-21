-- +goose Up
-- Before plugin migrations, a core migration made these tables without the plugin_ prefix. Make
-- them in their last shape only when they do not exist, then rename them, so an old database keeps
-- its rows.
CREATE TABLE IF NOT EXISTS github_webhook_settings (
  repository_id INTEGER PRIMARY KEY CHECK (repository_id > 0),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  issue_agent_id TEXT REFERENCES agents(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
CREATE TABLE IF NOT EXISTS github_review_pulls (
  repository_id INTEGER NOT NULL CHECK (repository_id > 0),
  pull_number INTEGER NOT NULL CHECK (pull_number > 0),
  token TEXT NOT NULL,
  configuration_id TEXT NOT NULL,
  etag TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (repository_id, pull_number)
) STRICT;
ALTER TABLE github_webhook_settings RENAME TO plugin_github_webhook_settings;
ALTER TABLE github_review_pulls RENAME TO plugin_github_review_pulls;

-- +goose Down
DROP TABLE plugin_github_review_pulls;
DROP TABLE plugin_github_webhook_settings;
