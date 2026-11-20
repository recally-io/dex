-- +goose Up
CREATE TABLE github_webhook_settings (
  repository_id INTEGER PRIMARY KEY CHECK (repository_id > 0),
  secret TEXT NOT NULL CHECK (length(secret) BETWEEN 32 AND 256)
) STRICT;

-- +goose Down
DROP TABLE github_webhook_settings;
