-- +goose Up
CREATE INDEX submissions_conversation_kind ON submissions (conversation_id, kind);

-- +goose Down
DROP INDEX submissions_conversation_kind;
