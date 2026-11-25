-- +goose Up
CREATE TABLE telegram_queue_acknowledgements (
    submission_id TEXT PRIMARY KEY NOT NULL REFERENCES submissions (id) ON DELETE CASCADE,
    outcome TEXT NOT NULL CHECK (outcome IN ('unknown', 'failed', 'accepted')),
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE telegram_queue_acknowledgements;
