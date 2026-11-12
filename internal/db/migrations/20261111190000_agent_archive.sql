-- +goose Up
-- An archived bot is hidden and idle. Its rows, files, and channel settings stay, so restore
-- needs no copy.
ALTER TABLE agents ADD COLUMN archived_at INTEGER;

-- A question can carry an action that control runs when the owner approves it (ADR 0016).
-- action_agent_id is the bot the action changes. approve_text is the one answer that approves.
-- outcome is the decision that control wrote into the answer; it is set once, with the answer.
ALTER TABLE questions ADD COLUMN action TEXT;
ALTER TABLE questions ADD COLUMN action_agent_id TEXT;
ALTER TABLE questions ADD COLUMN approve_text TEXT;
ALTER TABLE questions ADD COLUMN outcome TEXT;

-- +goose Down
ALTER TABLE questions DROP COLUMN outcome;
ALTER TABLE questions DROP COLUMN approve_text;
ALTER TABLE questions DROP COLUMN action_agent_id;
ALTER TABLE questions DROP COLUMN action;
ALTER TABLE agents DROP COLUMN archived_at;
