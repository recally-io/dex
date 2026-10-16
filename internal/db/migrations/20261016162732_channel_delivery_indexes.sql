-- +goose Up
CREATE INDEX submissions_channel_replies ON submissions (conversation_id, origin)
WHERE status != 'pending' AND delivered_at IS NULL
AND (kind = 'message' OR (kind = 'task_result' AND status = 'done'));

CREATE INDEX submissions_channel_pending ON submissions (origin, conversation_id)
WHERE status = 'pending';

-- +goose Down
DROP INDEX submissions_channel_pending;
DROP INDEX submissions_channel_replies;
