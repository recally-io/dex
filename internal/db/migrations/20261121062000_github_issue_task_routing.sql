-- +goose Up
ALTER TABLE github_webhook_settings ADD COLUMN issue_agent_id TEXT REFERENCES agents(id);

-- +goose Down
ALTER TABLE github_webhook_settings DROP COLUMN issue_agent_id;
