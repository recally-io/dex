-- +goose Up
CREATE TABLE system_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  update_channel TEXT NOT NULL DEFAULT 'stable' CHECK (update_channel IN ('stable', 'prerelease'))
) STRICT;
INSERT INTO system_settings (id, update_channel) VALUES (1, 'stable');

-- +goose Down
DROP TABLE system_settings;
