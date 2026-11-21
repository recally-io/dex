-- name: ListEnabledPlugins :many
SELECT name FROM enabled_plugins ORDER BY name;

-- name: EnablePlugin :exec
INSERT INTO enabled_plugins (name) VALUES (?) ON CONFLICT (name) DO NOTHING;

-- name: DisablePlugin :exec
DELETE FROM enabled_plugins WHERE name = ?;
