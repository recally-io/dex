-- +goose Up
CREATE TABLE mcp_servers (
    agent_id TEXT NOT NULL,
    name TEXT NOT NULL,
    config TEXT NOT NULL CHECK (json_valid(config)),
    position INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (agent_id, name)
) STRICT;

-- +goose Down
DROP TABLE mcp_servers;
