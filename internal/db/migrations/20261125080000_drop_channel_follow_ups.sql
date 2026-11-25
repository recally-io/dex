-- +goose Up
DROP TABLE channel_follow_ups;

-- +goose Down
CREATE TABLE channel_follow_ups (
    delegation_id TEXT PRIMARY KEY REFERENCES submissions (id) ON DELETE CASCADE,
    result_id TEXT NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'notified', 'missed_follow_up', 'cancelled')),
    child_id INTEGER REFERENCES conversations (id) ON DELETE CASCADE,
    parent_request_id TEXT REFERENCES submissions (id) ON DELETE CASCADE,
    parent_source_key TEXT,
    native_activation_id INTEGER,
    staged_report TEXT
) STRICT;
CREATE INDEX channel_follow_ups_conversation ON channel_follow_ups (conversation_id, state);
