-- +goose Up
CREATE TABLE task_sessions (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  bot_id TEXT NOT NULL REFERENCES agents(id),
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  turn_request_id TEXT NOT NULL DEFAULT '',
  is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
  state TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'completed', 'failed', 'cancelled')),
  started_at INTEGER NOT NULL,
  ended_at INTEGER
) STRICT;
CREATE UNIQUE INDEX task_sessions_link ON task_sessions(task_id, conversation_id);
CREATE UNIQUE INDEX task_sessions_primary ON task_sessions(task_id) WHERE is_primary = 1;
CREATE INDEX task_sessions_conversation ON task_sessions(conversation_id);
CREATE INDEX task_sessions_task ON task_sessions(task_id, is_primary DESC, started_at, id);

CREATE TABLE task_external_runs (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  bot_id TEXT NOT NULL REFERENCES agents(id),
  harness TEXT NOT NULL,
  runner_id TEXT NOT NULL DEFAULT '',
  host_meta_json TEXT NOT NULL DEFAULT '{}',
  workspace_path TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  external_session_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'completed', 'failed', 'stale', 'cancelled')),
  lease_epoch INTEGER NOT NULL DEFAULT 1,
  lease_expires_at INTEGER,
  lease_ttl_ms INTEGER NOT NULL DEFAULT 120000,
  last_heartbeat_at INTEGER,
  dispatch_id TEXT NOT NULL DEFAULT '',
  runner_enrollment TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  ended_at INTEGER
) STRICT;
CREATE UNIQUE INDEX task_external_runs_dispatch ON task_external_runs(dispatch_id) WHERE dispatch_id <> '';
CREATE INDEX task_external_runs_lease ON task_external_runs(lease_expires_at) WHERE state = 'running';
CREATE INDEX task_external_runs_task ON task_external_runs(task_id, started_at, id);

INSERT INTO task_sessions (id, task_id, bot_id, conversation_id, turn_request_id, is_primary, state, started_at, ended_at)
SELECT e.id, e.task_id, e.bot_id, e.conversation_id,
  coalesce(nullif(e.turn_request_id, ''), (
    SELECT s.id FROM submissions s JOIN conversations c ON c.id = s.conversation_id
    WHERE s.conversation_id = e.conversation_id AND c.parent_id IS NOT NULL
      AND s.kind = 'message' AND s.parent_request_id IS NOT NULL ORDER BY s.rowid LIMIT 1
  ), ''),
  e.is_primary, e.state, e.started_at, e.ended_at
FROM task_executions e WHERE e.kind = 'dex_session';

INSERT INTO task_external_runs (id, task_id, bot_id, harness, runner_id, host_meta_json, workspace_path,
  git_branch, external_session_id, state, lease_epoch, lease_expires_at, lease_ttl_ms, last_heartbeat_at,
  dispatch_id, runner_enrollment, started_at, ended_at)
SELECT id, task_id, bot_id, harness, runner_id, host_meta_json, workspace_path,
  git_branch, external_session_id, state, lease_epoch, lease_expires_at, lease_ttl_ms, last_heartbeat_at,
  dispatch_id, runner_enrollment, started_at, ended_at
FROM task_executions WHERE kind = 'external_agent';

-- Copy tokens before dropping the old parent table so the cascade cannot delete them.
CREATE TABLE task_execution_tokens_new (
  token_hash TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  execution_id TEXT NOT NULL REFERENCES task_external_runs(id) ON DELETE CASCADE,
  lease_epoch INTEGER NOT NULL,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL,
  dispatch_id TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO task_execution_tokens_new (token_hash, task_id, execution_id, lease_epoch, revoked_at, created_at, dispatch_id)
SELECT token_hash, task_id, execution_id, lease_epoch, revoked_at, created_at, dispatch_id FROM task_execution_tokens;
DROP TABLE task_execution_tokens;
ALTER TABLE task_execution_tokens_new RENAME TO task_execution_tokens;
CREATE UNIQUE INDEX task_execution_token_dispatch ON task_execution_tokens(dispatch_id) WHERE dispatch_id <> '';
CREATE INDEX task_execution_tokens_exec ON task_execution_tokens(execution_id, lease_epoch);
DROP TABLE task_executions;

-- +goose Down
CREATE TABLE task_executions (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  bot_id TEXT NOT NULL REFERENCES agents(id),
  kind TEXT NOT NULL CHECK (kind IN ('dex_session', 'external_agent')),
  conversation_id INTEGER REFERENCES conversations(id) ON DELETE CASCADE,
  turn_request_id TEXT NOT NULL DEFAULT '',
  is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
  harness TEXT NOT NULL DEFAULT '',
  runner_id TEXT NOT NULL DEFAULT '',
  host_meta_json TEXT NOT NULL DEFAULT '{}',
  workspace_path TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  external_session_id TEXT NOT NULL DEFAULT '',
  lease_epoch INTEGER NOT NULL DEFAULT 0,
  lease_expires_at INTEGER,
  last_heartbeat_at INTEGER,
  started_at INTEGER NOT NULL,
  ended_at INTEGER,
  dispatch_id TEXT NOT NULL DEFAULT '',
  lease_ttl_ms INTEGER NOT NULL DEFAULT 120000,
  runner_enrollment TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'completed', 'failed', 'stale', 'cancelled'))
) STRICT;
CREATE UNIQUE INDEX task_executions_primary ON task_executions(task_id) WHERE is_primary = 1;
CREATE UNIQUE INDEX task_executions_session ON task_executions(task_id, conversation_id) WHERE conversation_id IS NOT NULL;
CREATE INDEX task_executions_conversation ON task_executions(conversation_id) WHERE conversation_id IS NOT NULL;
CREATE INDEX task_executions_task ON task_executions(task_id, is_primary DESC, started_at, id);
CREATE UNIQUE INDEX task_execution_dispatch ON task_executions(dispatch_id) WHERE dispatch_id <> '';
CREATE INDEX task_execution_lease ON task_executions(lease_expires_at) WHERE kind = 'external_agent' AND state = 'running';

INSERT INTO task_executions (id, task_id, bot_id, kind, conversation_id, turn_request_id, is_primary, state, started_at, ended_at)
SELECT id, task_id, bot_id, 'dex_session', conversation_id, turn_request_id, is_primary, state, started_at, ended_at
FROM task_sessions;

INSERT INTO task_executions (id, task_id, bot_id, kind, harness, runner_id, host_meta_json, workspace_path,
  git_branch, external_session_id, state, lease_epoch, lease_expires_at, lease_ttl_ms, last_heartbeat_at,
  dispatch_id, runner_enrollment, started_at, ended_at)
SELECT id, task_id, bot_id, 'external_agent', harness, runner_id, host_meta_json, workspace_path,
  git_branch, external_session_id, state, lease_epoch, lease_expires_at, lease_ttl_ms, last_heartbeat_at,
  dispatch_id, runner_enrollment, started_at, ended_at
FROM task_external_runs;

CREATE TABLE task_execution_tokens_new (
  token_hash TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  execution_id TEXT NOT NULL REFERENCES task_executions(id) ON DELETE CASCADE,
  lease_epoch INTEGER NOT NULL,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL,
  dispatch_id TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO task_execution_tokens_new (token_hash, task_id, execution_id, lease_epoch, revoked_at, created_at, dispatch_id)
SELECT token_hash, task_id, execution_id, lease_epoch, revoked_at, created_at, dispatch_id FROM task_execution_tokens;
DROP TABLE task_execution_tokens;
ALTER TABLE task_execution_tokens_new RENAME TO task_execution_tokens;
CREATE UNIQUE INDEX task_execution_token_dispatch ON task_execution_tokens(dispatch_id) WHERE dispatch_id <> '';
CREATE INDEX task_execution_tokens_exec ON task_execution_tokens(execution_id, lease_epoch);
DROP TABLE task_sessions;
DROP TABLE task_external_runs;
