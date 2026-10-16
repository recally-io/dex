-- +goose Up
ALTER TABLE submissions ADD COLUMN origin TEXT NOT NULL DEFAULT 'unknown';

ALTER TABLE submissions RENAME COLUMN kind TO previous_kind;
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
    CHECK (kind IN ('message', 'task_result', 'context_reset'));
UPDATE submissions SET kind = previous_kind;
ALTER TABLE submissions DROP COLUMN previous_kind;

-- +goose Down
DELETE FROM submissions WHERE kind = 'context_reset';
ALTER TABLE submissions RENAME COLUMN kind TO previous_kind;
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
    CHECK (kind IN ('message', 'task_result'));
UPDATE submissions SET kind = previous_kind;
ALTER TABLE submissions DROP COLUMN previous_kind;
ALTER TABLE submissions DROP COLUMN origin;
