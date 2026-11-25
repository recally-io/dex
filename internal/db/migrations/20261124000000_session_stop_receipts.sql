-- +goose Up
CREATE TABLE main_turn_stops (
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    target TEXT NOT NULL,
    PRIMARY KEY (conversation_id, target)
) STRICT;

-- +goose Down
DROP TABLE main_turn_stops;
