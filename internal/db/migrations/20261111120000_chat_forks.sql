-- +goose Up
-- The chat that a fork copied its history from. A fork has history before its first message, so
-- it is never reused as an empty chat and it shows in the session list.
ALTER TABLE conversations ADD COLUMN forked_from INTEGER;

-- +goose Down
ALTER TABLE conversations DROP COLUMN forked_from;
