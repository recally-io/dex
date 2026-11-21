-- +goose Up
-- The plugin_ prefix is reserved for plugin tables. A row turns a plugin on.
CREATE TABLE enabled_plugins (
  name TEXT PRIMARY KEY CHECK (name GLOB '[a-z]*' AND name NOT GLOB '*[^a-z]*')
) STRICT;
INSERT INTO enabled_plugins (name) SELECT name FROM plugin_settings WHERE enabled = 1;
DROP TABLE plugin_settings;

-- The applied migrations of every plugin. Version 0 means the plugin has started to migrate.
-- Each plugin numbers its own versions.
CREATE TABLE applied_plugin_migrations (
  plugin TEXT NOT NULL CHECK (plugin GLOB '[a-z]*' AND plugin NOT GLOB '*[^a-z]*'),
  version INTEGER NOT NULL CHECK (version >= 0),
  applied_at INTEGER NOT NULL,
  PRIMARY KEY (plugin, version)
) STRICT;

-- +goose Down
-- Fail when a plugin has migrated. Its renamed tables would stay without their versions, so the
-- next Up would skip them. Restore the data backup from before the upgrade instead.
CREATE TEMP TABLE plugin_downgrade_guard (migrated INTEGER CHECK (migrated = 0));
INSERT INTO plugin_downgrade_guard SELECT count(*) FROM applied_plugin_migrations;
DROP TABLE plugin_downgrade_guard;
DROP TABLE applied_plugin_migrations;
CREATE TABLE plugin_settings (
  name TEXT PRIMARY KEY CHECK (name GLOB '[a-z]*' AND name NOT GLOB '*[^a-z]*'),
  enabled INTEGER NOT NULL CHECK (enabled IN (0, 1))
) STRICT;
INSERT INTO plugin_settings (name, enabled) SELECT name, 1 FROM enabled_plugins;
DROP TABLE enabled_plugins;
