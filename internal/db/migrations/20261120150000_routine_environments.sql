-- +goose Up
-- The execution environment of a routine's runs: local or a pi-env environment ID. Empty follows
-- the environment of the agent.
ALTER TABLE automations ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE automations DROP COLUMN environment_id;
