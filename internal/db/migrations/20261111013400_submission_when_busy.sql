-- +goose Up
-- What native admission does with the input when the chat already runs: followUp waits for the
-- run to end, steer joins the run after its current tool step, reject refuses the input.
ALTER TABLE submissions ADD COLUMN when_busy TEXT NOT NULL DEFAULT 'followUp'
    CHECK (when_busy IN ('followUp', 'steer', 'reject'));

-- +goose Down
ALTER TABLE submissions DROP COLUMN when_busy;
