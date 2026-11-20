-- +goose Up
ALTER TABLE questions ADD COLUMN queued INTEGER NOT NULL DEFAULT 0 CHECK (queued IN (0, 1));
DROP INDEX questions_one_open;
CREATE UNIQUE INDEX questions_one_open ON questions (conversation_id) WHERE status = 'open' AND queued = 0;

-- +goose Down
UPDATE questions SET status = 'dismissed' WHERE queued = 1 AND status IN ('open', 'answered');
DROP INDEX questions_one_open;
ALTER TABLE questions DROP COLUMN queued;
CREATE UNIQUE INDEX questions_one_open ON questions (conversation_id) WHERE status = 'open';
