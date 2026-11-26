-- +goose Up
ALTER TABLE task_executions ADD COLUMN dispatch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_executions ADD COLUMN lease_ttl_ms INTEGER NOT NULL DEFAULT 120000;
ALTER TABLE task_executions ADD COLUMN runner_enrollment TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX task_execution_dispatch ON task_executions(dispatch_id) WHERE dispatch_id <> '';
CREATE INDEX task_execution_lease ON task_executions(lease_expires_at)
  WHERE kind = 'external_agent' AND state = 'running';

CREATE TABLE task_execution_tokens (
  token_hash TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES work_tasks(id),
  execution_id TEXT NOT NULL REFERENCES task_executions(id) ON DELETE CASCADE,
  lease_epoch INTEGER NOT NULL,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL,
  dispatch_id TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX task_execution_token_dispatch ON task_execution_tokens(dispatch_id) WHERE dispatch_id <> '';
CREATE INDEX task_execution_tokens_exec ON task_execution_tokens(execution_id, lease_epoch);

-- +goose Down
DROP TABLE task_execution_tokens;
DROP INDEX task_execution_lease;
DROP INDEX task_execution_dispatch;
ALTER TABLE task_executions DROP COLUMN runner_enrollment;
ALTER TABLE task_executions DROP COLUMN lease_ttl_ms;
ALTER TABLE task_executions DROP COLUMN dispatch_id;
