-- +goose Up
ALTER TABLE agents ADD COLUMN fallback_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN fallback_model_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agents DROP COLUMN fallback_model_id;
ALTER TABLE agents DROP COLUMN fallback_provider;
