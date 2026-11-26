-- name: ListMCPServers :many
SELECT bot_id, name, config FROM mcp_servers
WHERE bot_id = '' OR bot_id = ?
ORDER BY bot_id, position;

-- name: GetMCPServer :one
SELECT config FROM mcp_servers WHERE bot_id = ? AND name = ?;

-- name: SetMCPServer :exec
INSERT INTO mcp_servers (bot_id, name, config, position, updated_at)
VALUES (sqlc.arg(bot_id), sqlc.arg(name), sqlc.arg(config),
    (SELECT coalesce(max(position), 0) + 1 FROM mcp_servers WHERE bot_id = sqlc.arg(bot_id)),
    unixepoch())
ON CONFLICT (bot_id, name) DO UPDATE SET config = excluded.config, updated_at = excluded.updated_at;

-- name: RemoveMCPServer :execrows
DELETE FROM mcp_servers WHERE bot_id = ? AND name = ?;
