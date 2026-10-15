-- +goose Up
-- The default thinking level, pi-ai's ModelThinkingLevel. pi-durable runs a conversation without a
-- level as 'off', so 'off' keeps the behavior of existing data.
ALTER TABLE agents ADD COLUMN thinking_level TEXT NOT NULL DEFAULT 'off' CHECK (
    thinking_level IN ('off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max')
);

-- The settings that one chat overrides. An empty value follows the agent's setting, so a later change
-- of the agent applies to the chat. model_provider and model_id are both empty or both set.
ALTER TABLE conversations ADD COLUMN model_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '' CHECK (
    thinking_level IN ('', 'off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max')
);

-- The files of a message, as the JSON array of the API's Attachment. The files are in the agent
-- workspace; a replay after a crash sends the same list.
ALTER TABLE submissions ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE submissions DROP COLUMN attachments;
ALTER TABLE conversations DROP COLUMN thinking_level;
ALTER TABLE conversations DROP COLUMN model_id;
ALTER TABLE conversations DROP COLUMN model_provider;
ALTER TABLE agents DROP COLUMN thinking_level;
