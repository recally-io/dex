-- name: ListMCPServers :many
SELECT agent_id, name, config FROM mcp_servers
WHERE agent_id = '' OR agent_id = ?
ORDER BY agent_id, position;

-- name: GetMCPServer :one
SELECT config FROM mcp_servers WHERE agent_id = ? AND name = ?;

-- name: SetMCPServer :exec
INSERT INTO mcp_servers (agent_id, name, config, position, updated_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(name), sqlc.arg(config),
    (SELECT coalesce(max(position), 0) + 1 FROM mcp_servers WHERE agent_id = sqlc.arg(agent_id)),
    unixepoch())
ON CONFLICT (agent_id, name) DO UPDATE SET config = excluded.config, updated_at = excluded.updated_at;

-- name: RemoveMCPServer :execrows
DELETE FROM mcp_servers WHERE agent_id = ? AND name = ?;
