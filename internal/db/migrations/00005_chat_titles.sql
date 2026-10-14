-- +goose Up
-- The chat list of the web UI shows a title: the start of the chat's first message.
ALTER TABLE conversations ADD COLUMN title TEXT NOT NULL DEFAULT '';

UPDATE conversations SET title = coalesce((
    SELECT substr(s.content, 1, 60) FROM submissions s
    WHERE s.conversation_id = conversations.id
    ORDER BY s.created_at LIMIT 1
), '');

-- +goose Down
ALTER TABLE conversations DROP COLUMN title;
