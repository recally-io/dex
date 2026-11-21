-- The plugin tables as core migrations made them before plugin migrations existed, in their order.
CREATE TABLE github_webhook_settings (
  repository_id INTEGER PRIMARY KEY CHECK (repository_id > 0),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256)
) STRICT;
CREATE TABLE github_review_pulls (
  repository_id INTEGER NOT NULL CHECK (repository_id > 0),
  pull_number INTEGER NOT NULL CHECK (pull_number > 0),
  token TEXT NOT NULL,
  configuration_id TEXT NOT NULL,
  etag TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (repository_id, pull_number)
) STRICT;
ALTER TABLE github_webhook_settings ADD COLUMN issue_agent_id TEXT REFERENCES agents(id);
ALTER TABLE github_webhook_settings ADD COLUMN hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1));
CREATE TABLE webhook_sources (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_agent_id TEXT REFERENCES agents(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
CREATE TABLE email_sources (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64 AND id NOT GLOB '*[^a-z0-9-]*' AND id NOT GLOB '-*'),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256),
  task_agent_id TEXT REFERENCES agents(id),
  hold_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (hold_unmatched IN (0, 1))
) STRICT;
CREATE TABLE email_imap_pulls (
  source_id TEXT PRIMARY KEY REFERENCES email_sources(id) ON DELETE CASCADE,
  address TEXT NOT NULL CHECK (length(address) BETWEEN 1 AND 300),
  username TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 320),
  password TEXT NOT NULL CHECK (length(password) BETWEEN 1 AND 1024),
  folder TEXT NOT NULL CHECK (length(folder) BETWEEN 1 AND 255),
  configuration_id TEXT NOT NULL,
  uid_validity INTEGER NOT NULL DEFAULT 0,
  last_uid INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO goose_db_version (version_id, is_applied) VALUES
  (20261121041000, 1), (20261121051000, 1), (20261121062000, 1), (20261121160000, 1), (20261121170000, 1), (20261121190000, 1);
