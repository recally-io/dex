-- +goose Up
-- The account that an enrolled machine executes as, when the installer runs the machine as the
-- account that installed it instead of a dedicated account. Empty means a dedicated account,
-- which is what every earlier install does.
ALTER TABLE execution_environments ADD COLUMN run_as_user TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE execution_environments DROP COLUMN run_as_user;
