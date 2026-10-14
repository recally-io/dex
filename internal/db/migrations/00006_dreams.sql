-- +goose Up
-- One dreaming run. The run is a hidden chat (channel key dream:<id>) with one message, whose ID is
-- the dream's ID; the message holds the chats since the previous dream. When the message is done,
-- the dream consumed the settled messages up to (through_settled_at, through_id).
CREATE TABLE dreams (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    through_settled_at INTEGER NOT NULL,
    through_id TEXT NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE dreams;
