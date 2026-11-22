-- +goose Up
-- Replace only the checked column. Keep the table and its dependent runs and triggers.
ALTER TABLE automations ADD COLUMN previous_session TEXT;
UPDATE automations SET previous_session = session;
ALTER TABLE automations DROP COLUMN session;
ALTER TABLE automations ADD COLUMN session TEXT NOT NULL DEFAULT 'new' CHECK (session IN ('new', 'reuse', 'session'));
UPDATE automations SET session = previous_session;
ALTER TABLE automations DROP COLUMN previous_session;
ALTER TABLE automations ADD COLUMN target_conversation_id INTEGER REFERENCES conversations(id) ON DELETE SET NULL;
CREATE INDEX automations_target_conversation ON automations(target_conversation_id) WHERE target_conversation_id IS NOT NULL;

-- +goose Down
-- Disable bound routines rather than let them run in a new conversation on downgrade.
UPDATE automations SET session = 'new', enabled = 0, next_due_at = NULL WHERE session = 'session';
DROP INDEX automations_target_conversation;
ALTER TABLE automations DROP COLUMN target_conversation_id;
ALTER TABLE automations ADD COLUMN previous_session TEXT;
UPDATE automations SET previous_session = session;
ALTER TABLE automations DROP COLUMN session;
ALTER TABLE automations ADD COLUMN session TEXT NOT NULL DEFAULT 'new' CHECK (session IN ('new', 'reuse'));
UPDATE automations SET session = previous_session;
ALTER TABLE automations DROP COLUMN previous_session;
