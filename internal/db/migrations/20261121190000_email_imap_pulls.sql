-- +goose Up
-- IMAP Pull reads one folder of an email source. uid_validity 0 means the cursor is not set yet.
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

-- +goose Down
DROP TABLE email_imap_pulls;
