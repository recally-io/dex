-- +goose Up
-- The execution environment of a session. '' follows the environment of the bot. 'local' or an
-- execution_environments ID is an explicit selection: it keeps its binding when the bot's
-- environment changes. It has no foreign key, because 'local' has no row and a deleted
-- environment must leave the session readable.
ALTER TABLE conversations ADD COLUMN environment_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE conversations DROP COLUMN environment_id;
