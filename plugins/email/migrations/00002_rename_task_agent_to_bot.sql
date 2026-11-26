-- +goose Up
-- On a new install, version 1 still refers to agents. On upgrade, SQLite has
-- already changed that reference to bots. Rebuild the table for both cases.
CREATE TABLE plugin_email_sources_new (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_bot_id TEXT REFERENCES bots(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
INSERT INTO plugin_email_sources_new SELECT * FROM plugin_email_sources;
CREATE TABLE plugin_email_imap_pulls_new (
  source_id TEXT PRIMARY KEY REFERENCES plugin_email_sources_new(id) ON DELETE CASCADE,
  address TEXT NOT NULL CHECK (length(address) BETWEEN 1 AND 300),
  username TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 320),
  password TEXT NOT NULL CHECK (length(password) BETWEEN 1 AND 1024),
  folder TEXT NOT NULL CHECK (length(folder) BETWEEN 1 AND 255),
  configuration_id TEXT NOT NULL,
  uid_validity INTEGER NOT NULL DEFAULT 0,
  last_uid INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO plugin_email_imap_pulls_new SELECT * FROM plugin_email_imap_pulls;
DROP TABLE plugin_email_imap_pulls;
DROP TABLE plugin_email_sources;
ALTER TABLE plugin_email_sources_new RENAME TO plugin_email_sources;
ALTER TABLE plugin_email_imap_pulls_new RENAME TO plugin_email_imap_pulls;

-- +goose Down
-- The core downgrade changes the reference from bots back to agents.
CREATE TABLE plugin_email_sources_new (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_agent_id TEXT REFERENCES bots(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
INSERT INTO plugin_email_sources_new SELECT * FROM plugin_email_sources;
CREATE TABLE plugin_email_imap_pulls_new (
  source_id TEXT PRIMARY KEY REFERENCES plugin_email_sources_new(id) ON DELETE CASCADE,
  address TEXT NOT NULL CHECK (length(address) BETWEEN 1 AND 300),
  username TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 320),
  password TEXT NOT NULL CHECK (length(password) BETWEEN 1 AND 1024),
  folder TEXT NOT NULL CHECK (length(folder) BETWEEN 1 AND 255),
  configuration_id TEXT NOT NULL,
  uid_validity INTEGER NOT NULL DEFAULT 0,
  last_uid INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO plugin_email_imap_pulls_new SELECT * FROM plugin_email_imap_pulls;
DROP TABLE plugin_email_imap_pulls;
DROP TABLE plugin_email_sources;
ALTER TABLE plugin_email_sources_new RENAME TO plugin_email_sources;
ALTER TABLE plugin_email_imap_pulls_new RENAME TO plugin_email_imap_pulls;
