-- +goose Up
-- The owner turns each optional plugin on or off. dex reads it at start.
-- A plugin without a row is off. GitHub ran before plugins existed, so it starts on.
CREATE TABLE plugin_settings (
  name TEXT PRIMARY KEY CHECK (name GLOB '[a-z]*' AND name NOT GLOB '*[^a-z]*'),
  enabled INTEGER NOT NULL CHECK (enabled IN (0, 1))
) STRICT;
INSERT INTO plugin_settings (name, enabled) VALUES ('github', 1);

-- +goose Down
DROP TABLE plugin_settings;
