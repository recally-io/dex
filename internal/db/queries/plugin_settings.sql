-- name: ListEnabledPlugins :many
SELECT name FROM plugin_settings WHERE enabled = 1 ORDER BY name;

-- name: SetPluginEnabled :exec
INSERT INTO plugin_settings (name, enabled) VALUES (?, ?)
ON CONFLICT (name) DO UPDATE SET enabled = excluded.enabled;
