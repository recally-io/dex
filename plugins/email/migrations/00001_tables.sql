-- +goose Up
-- Before plugin migrations, a core migration made these tables without the plugin_ prefix. Make
-- them only when they do not exist, then rename them, so an old database keeps its rows. SQLite
-- also renames the reference from the IMAP Pull to its source.
-- A signed email source posts raw messages of one address. Its ID is also its event namespace.
CREATE TABLE IF NOT EXISTS email_sources (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_agent_id TEXT REFERENCES agents(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
-- IMAP Pull reads one folder of an email source. uid_validity 0 means the cursor is not set yet.
CREATE TABLE IF NOT EXISTS email_imap_pulls (
  source_id TEXT PRIMARY KEY REFERENCES email_sources(id) ON DELETE CASCADE,
  address TEXT NOT NULL CHECK (length(address) BETWEEN 1 AND 300),
  username TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 320),
  password TEXT NOT NULL CHECK (length(password) BETWEEN 1 AND 1024),
  folder TEXT NOT NULL CHECK (length(folder) BETWEEN 1 AND 255),
  configuration_id TEXT NOT NULL,
  uid_validity INTEGER NOT NULL DEFAULT 0,
  last_uid INTEGER NOT NULL DEFAULT 0
) STRICT;
ALTER TABLE email_sources RENAME TO plugin_email_sources;
ALTER TABLE email_imap_pulls RENAME TO plugin_email_imap_pulls;

-- +goose Down
DROP TABLE plugin_email_imap_pulls;
DROP TABLE plugin_email_sources;
