-- +goose Up
CREATE TABLE compact_receipts (
    id TEXT PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    instructions TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE compact_receipts;
