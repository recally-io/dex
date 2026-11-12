-- +goose Up
CREATE TABLE fork_receipts (
    id TEXT PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES conversations(id),
    entry_id INTEGER NOT NULL,
    before_entry INTEGER NOT NULL,
    fork_id INTEGER NOT NULL REFERENCES conversations(id)
) STRICT;

-- +goose Down
DROP TABLE fork_receipts;
