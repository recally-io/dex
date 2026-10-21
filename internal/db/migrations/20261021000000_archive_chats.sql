-- +goose Up
-- When the user archived a Chat session, in Unix milliseconds. NULL when it is not archived.
-- Archiving hides nothing from storage: the messages, records and files stay.
ALTER TABLE conversations ADD COLUMN archived_at INTEGER;

-- +goose Down
ALTER TABLE conversations DROP COLUMN archived_at;
