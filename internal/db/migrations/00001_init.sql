-- +goose Up
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    model_provider TEXT NOT NULL,
    model_id TEXT NOT NULL,
    instructions TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
) STRICT;

-- One chat on one channel, for example "web:default" or "telegram:<chat id>".
CREATE TABLE conversations (
    id INTEGER PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    channel_key TEXT NOT NULL,
    -- The pi-durable conversation in the worker. NULL until the worker creates it.
    worker_conversation_id INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (agent_id, channel_key)
) STRICT;

-- One user message. Control writes it before it sends it to the worker, so a message survives a
-- worker crash. The ID is also the pi-durable requestId.
CREATE TABLE submissions (
    id TEXT PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    content TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'done', 'unanswered')),
    answer TEXT,
    reason TEXT,
    created_at INTEGER NOT NULL,
    settled_at INTEGER
) STRICT;

CREATE INDEX submissions_pending ON submissions (created_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE submissions;
DROP TABLE conversations;
DROP TABLE agents;
