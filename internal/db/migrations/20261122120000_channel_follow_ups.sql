-- +goose Up
CREATE TABLE channel_follow_ups (
    delegation_id TEXT PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE,
    result_id TEXT NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'notified', 'silent', 'missed_follow_up', 'cancelled'))
) STRICT;
CREATE INDEX channel_follow_ups_parent ON channel_follow_ups(conversation_id);

-- +goose Down
DROP TABLE channel_follow_ups;
