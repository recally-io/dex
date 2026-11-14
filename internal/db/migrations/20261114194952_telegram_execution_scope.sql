-- +goose Up
ALTER TABLE conversations ADD COLUMN execution_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN scope_context TEXT NOT NULL DEFAULT '{}';
CREATE UNIQUE INDEX conversations_native_scope ON conversations (agent_id, execution_scope, execution_id) WHERE execution_id IS NOT NULL;

-- +goose Down
DROP INDEX conversations_native_scope;
ALTER TABLE conversations DROP COLUMN scope_context;
ALTER TABLE conversations DROP COLUMN execution_scope;
