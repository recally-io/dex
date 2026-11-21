-- +goose Up
CREATE TABLE github_review_pulls (
  repository_id INTEGER NOT NULL CHECK (repository_id > 0),
  pull_number INTEGER NOT NULL CHECK (pull_number > 0),
  token TEXT NOT NULL,
  configuration_id TEXT NOT NULL,
  etag TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (repository_id, pull_number)
) STRICT;

-- +goose Down
DROP TABLE github_review_pulls;
