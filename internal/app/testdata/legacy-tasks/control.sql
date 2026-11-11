PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
INSERT INTO goose_db_version VALUES(1,0,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(2,1,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(3,2,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(4,3,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(5,4,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(6,5,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(7,6,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(8,7,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(9,8,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(10,9,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(11,20261015100000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(12,20261015113034,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(13,20261015120000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(14,20261016000000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(15,20261016010000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(16,20261016084115,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(17,20261016144337,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(18,20261016162732,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(19,20261017000000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(20,20261020000000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(21,20261021000000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(22,20261026040647,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(23,20261026084339,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(24,20261026095344,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(25,20261026110000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(26,20261026144434,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(27,20261028120000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(28,20261103092655,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(29,20261109172156,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(30,20261110022858,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(31,20261110120000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(32,20261110130000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(33,20261110140000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(34,20261110142827,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(35,20261111013400,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(36,20261111120000,1,'2026-11-11 13:51:22');
INSERT INTO goose_db_version VALUES(37,20261111150000,1,'2026-11-11 13:51:22');
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    model_provider TEXT NOT NULL,
    model_id TEXT NOT NULL,
    instructions TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
, thinking_level TEXT NOT NULL DEFAULT 'off' CHECK (
    thinking_level IN ('off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max')
), primary_notification_target_id TEXT REFERENCES routine_notification_targets(id) ON DELETE SET NULL) STRICT;
INSERT INTO agents VALUES('default','Dex','faux','spawn','',1794405082925,'off',NULL);
CREATE TABLE conversations (
    id INTEGER PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    channel_key TEXT NOT NULL,
    -- The pi-durable conversation in the worker. NULL until the worker creates it.
    execution_id INTEGER,
    created_at INTEGER NOT NULL, title TEXT NOT NULL DEFAULT '', model_provider TEXT NOT NULL DEFAULT '', model_id TEXT NOT NULL DEFAULT '', thinking_level TEXT NOT NULL DEFAULT '' CHECK (
    thinking_level IN ('', 'off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max')
), abort_pending INTEGER NOT NULL DEFAULT 0 CHECK (abort_pending IN (0, 1)), archived_at INTEGER, cwd TEXT NOT NULL DEFAULT '', forked_from INTEGER,
    UNIQUE (agent_id, channel_key)
) STRICT;
INSERT INTO conversations VALUES(1,'default','chat:01a1cae0-a737-7a2d-9753-3f2cd2a1ffd2',2,1794405082935,'{"modelId":"echo","title":"Ended","goal":"ended goal","prov…','','','',0,NULL,'',NULL);
INSERT INTO conversations VALUES(2,'default','task:01a1cae0-a7da-7a31-adac-a4892a20b8f9',16,1794405083098,'Ended','faux','echo','off',0,NULL,'',NULL);
INSERT INTO conversations VALUES(3,'default','task:01a1cae0-a992-7eb0-9adf-c25a6ed3de56',40,1794405083538,'Continued','faux','echo','off',0,NULL,'',NULL);
INSERT INTO conversations VALUES(4,'default','task:01a1cae0-ae3e-7e97-813b-875aafda8c4b',69,1794405084734,'Running','faux','bash','off',0,NULL,'',NULL);
CREATE TABLE submissions (
    id TEXT PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    content TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'done', 'unanswered')),
    answer TEXT,
    reason TEXT,
    created_at INTEGER NOT NULL,
    settled_at INTEGER
, source_key TEXT, attachments TEXT NOT NULL DEFAULT '[]', delivered_at INTEGER, delivered_parts INTEGER NOT NULL DEFAULT 0, origin TEXT NOT NULL DEFAULT 'unknown', kind TEXT NOT NULL DEFAULT 'message'
    CHECK (kind IN ('message', 'task_result', 'context_reset')), admitted_at INTEGER, when_busy TEXT NOT NULL DEFAULT 'followUp'
    CHECK (when_busy IN ('followUp', 'steer', 'reject'))) STRICT;
INSERT INTO submissions VALUES('01a1cae0-a738-71e3-8bb0-b98de4b443c0',1,'{"modelId":"echo","title":"Ended","goal":"ended goal","provider":"faux"}','done','spawn: {"id":"01a1cae0-a7da-7a31-adac-a4892a20b8f9"}',NULL,1794405082936,1794405083183,NULL,'[]',NULL,0,'web','message',1794405082993,'followUp');
INSERT INTO submissions VALUES('01a1cae0-a7da-7a31-adac-a4892a20b8f9',2,'ended goal','done','echo: ended goal',NULL,1794405083098,1794405083136,NULL,'null',NULL,0,'task','message',1794405083102,'followUp');
INSERT INTO submissions VALUES('task:01a1cae0-a7da-7a31-adac-a4892a20b8f9:result',1,'{"task-result":{"id":"01a1cae0-a7da-7a31-adac-a4892a20b8f9","title":"Ended","status":"done","report":"echo: ended goal","hasStructuredReport":false,"goalMet":false}}','done','task-result: {"goalMet":false,"hasStructuredReport":false,"id":"01a1cae0-a7da-7a31-adac-a4892a20b8f9","report":"echo: ended goal","status":"done","title":"Ended"}',NULL,1794405083136,1794405083440,'task:01a1cae0-a7da-7a31-adac-a4892a20b8f9:result','null',NULL,0,'web','task_result',1794405083138,'followUp');
INSERT INTO submissions VALUES('01a1cae0-a809-7849-8969-ded2302c99b0',1,'{"title":"Continued","goal":"continued goal","provider":"faux","modelId":"echo"}','done','spawn: {"id":"01a1cae0-a992-7eb0-9adf-c25a6ed3de56"}',NULL,1794405083145,1794405083651,NULL,'[]',NULL,0,'web','message',1794405083146,'followUp');
INSERT INTO submissions VALUES('01a1cae0-a992-7eb0-9adf-c25a6ed3de56',3,'continued goal','done','echo: continued goal',NULL,1794405083538,1794405083613,NULL,'null',NULL,0,'task','message',1794405083550,'followUp');
INSERT INTO submissions VALUES('task:01a1cae0-a992-7eb0-9adf-c25a6ed3de56:result',1,'{"task-result":{"id":"01a1cae0-a992-7eb0-9adf-c25a6ed3de56","title":"Continued","status":"done","report":"echo: continued goal","hasStructuredReport":false,"goalMet":false}}','done','task-result: {"goalMet":false,"hasStructuredReport":false,"id":"01a1cae0-a992-7eb0-9adf-c25a6ed3de56","report":"echo: continued goal","status":"done","title":"Continued"}',NULL,1794405083613,1794405083905,'task:01a1cae0-a992-7eb0-9adf-c25a6ed3de56:result','null',NULL,0,'web','task_result',1794405083616,'followUp');
INSERT INTO submissions VALUES('0194f2c0-0000-7000-8000-00000000c0de',3,'thanks','done','echo: thanks',NULL,1794405083886,1794405083932,NULL,'[]',NULL,0,'web','message',1794405083890,'followUp');
INSERT INTO submissions VALUES('01a1cae0-ab0a-711e-b9c1-2463a52d371e',1,'{"title":"Running","goal":"printf ''real artifact\\n'' > ''/var/folders/_2/mc7147l95fsfyr9wbm7dkt040000gn/T/TestGenerateLegacyTaskFixture3043457903/001/legacy.output''; echo $$ > ''/var/folders/_2/mc7147l95fsfyr9wbm7dkt040000gn/T/TestGenerateLegacyTaskFixture3043457903/001/legacy.pid''; deadline=$((SECONDS+45)); until test -f ''/var/folders/_2/mc7147l95fsfyr9wbm7dkt040000gn/T/TestGenerateLegacyTaskFixture3043457903/001/legacy.gate''; do if (( SECONDS >= deadline )); then echo ''gate deadline exceeded'' >&2; exit 1; fi; sleep 0.05; done; echo ''gate released''","provider":"faux","modelId":"bash"}','done','spawn: {"id":"01a1cae0-ae3e-7e97-813b-875aafda8c4b"}',NULL,1794405083916,1794405084862,NULL,'[]',NULL,0,'web','message',1794405083919,'followUp');
INSERT INTO submissions VALUES('01a1cae0-ae3e-7e97-813b-875aafda8c4b',4,'printf ''real artifact\n'' > ''/var/folders/_2/mc7147l95fsfyr9wbm7dkt040000gn/T/TestGenerateLegacyTaskFixture3043457903/001/legacy.output''; echo $$ > ''/var/folders/_2/mc7147l95fsfyr9wbm7dkt040000gn/T/TestGenerateLegacyTaskFixture3043457903/001/legacy.pid''; deadline=$((SECONDS+45)); until test -f ''/var/folders/_2/mc7147l95fsfyr9wbm7dkt040000gn/T/TestGenerateLegacyTaskFixture3043457903/001/legacy.gate''; do if (( SECONDS >= deadline )); then echo ''gate deadline exceeded'' >&2; exit 1; fi; sleep 0.05; done; echo ''gate released''','pending',NULL,NULL,1794405084734,NULL,NULL,'null',NULL,0,'task','message',1794405084743,'followUp');
CREATE TABLE providers (
    -- The pi-ai provider ID. The user chooses it for a custom provider.
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    api TEXT NOT NULL CHECK (
        api IN ('', 'openai-completions', 'openai-responses', 'anthropic-messages')
    ),
    base_url TEXT NOT NULL DEFAULT '',
    api_key TEXT NOT NULL DEFAULT '',
    -- Model IDs separated by commas. Empty: list the models from the endpoint.
    models TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
) STRICT;
CREATE TABLE dreams (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    through_settled_at INTEGER NOT NULL,
    through_id TEXT NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
CREATE TABLE automations (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    schedule TEXT NOT NULL,
    time_zone TEXT NOT NULL,
    session TEXT NOT NULL CHECK (session IN ('new', 'reuse')),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    next_due_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
, cwd TEXT NOT NULL DEFAULT '', notification_route TEXT NOT NULL DEFAULT 'none' CHECK (notification_route IN ('none', 'default', 'target')), notification_target_id TEXT REFERENCES routine_notification_targets(id) ON DELETE SET NULL) STRICT;
CREATE TABLE automation_runs (
    id TEXT PRIMARY KEY,
    automation_id TEXT NOT NULL REFERENCES automations (id),
    due_at INTEGER,
    status TEXT NOT NULL CHECK (status IN ('started', 'failed', 'skipped')),
    reason TEXT,
    created_at INTEGER NOT NULL
, outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'changed', 'unchanged', 'failed')), summary TEXT NOT NULL DEFAULT '', change_key TEXT NOT NULL DEFAULT '', duplicate INTEGER NOT NULL DEFAULT 0 CHECK (duplicate IN (0, 1)), read_at INTEGER, dismissed_at INTEGER) STRICT;
CREATE TABLE access_token (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT NOT NULL
) STRICT;
CREATE TABLE channel_peers (
    peer TEXT PRIMARY KEY,
    channel_key TEXT NOT NULL
) STRICT;
CREATE TABLE wechat_runtime (
    bot_id TEXT NOT NULL,
    owner TEXT NOT NULL,
    cursor TEXT NOT NULL DEFAULT '',
    context_token TEXT NOT NULL DEFAULT '',
    context_message_id TEXT NOT NULL DEFAULT '00000000000000000000',
    pause_until INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bot_id, owner)
) STRICT;
CREATE TABLE IF NOT EXISTS "telegram_settings" (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id),
    token TEXT NOT NULL UNIQUE,
    allowed_users TEXT NOT NULL,
    pairing_code TEXT NOT NULL DEFAULT '',
    pairing_expires INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE IF NOT EXISTS "wechat_settings" (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id),
    token TEXT NOT NULL,
    base_url TEXT NOT NULL,
    bot_id TEXT NOT NULL UNIQUE,
    owner TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS "feishu_settings" (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id),
    app_id TEXT NOT NULL UNIQUE,
    app_secret TEXT NOT NULL,
    brand TEXT NOT NULL CHECK (brand IN ('feishu', 'lark')),
    owner_id TEXT NOT NULL DEFAULT '',
    pairing_code TEXT NOT NULL DEFAULT '',
    pairing_expires_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE IF NOT EXISTS "channel_commands" (
    agent_id TEXT NOT NULL REFERENCES agents(id),
    source_key TEXT PRIMARY KEY,
    peer TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    command TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    completed INTEGER NOT NULL DEFAULT 0,
    delivered_at INTEGER
) STRICT;
CREATE TABLE routine_tool_operations (
    key TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    source_request_id TEXT NOT NULL REFERENCES submissions(id),
    action TEXT NOT NULL,
    result TEXT NOT NULL
) STRICT;
CREATE TABLE tasks (
    id TEXT PRIMARY KEY REFERENCES submissions (id),
    parent_conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    conversation_id INTEGER NOT NULL UNIQUE REFERENCES conversations (id),
    spawn_key TEXT NOT NULL UNIQUE,
    source_request_id TEXT NOT NULL REFERENCES submissions (id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'admitting', 'running', 'done', 'failed', 'cancelled', 'interrupted')),
    report TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    ended_at INTEGER,
    constraints_json TEXT NOT NULL DEFAULT '[]',
    done_criteria_json TEXT NOT NULL DEFAULT '[]',
    structured_report TEXT NOT NULL DEFAULT '',
    modifies_files INTEGER NOT NULL DEFAULT 1
, handled_at INTEGER) STRICT;
INSERT INTO tasks VALUES('01a1cae0-a7da-7a31-adac-a4892a20b8f9',1,2,'default/14','01a1cae0-a738-71e3-8bb0-b98de4b443c0','Ended','done','echo: ended goal',1794405083098,1794405083099,1794405083136,'[]','[]','',1,NULL);
INSERT INTO tasks VALUES('01a1cae0-a992-7eb0-9adf-c25a6ed3de56',1,3,'default/38','01a1cae0-a809-7849-8969-ded2302c99b0','Continued','done','echo: continued goal',1794405083538,1794405083541,1794405083613,'[]','[]','',1,NULL);
INSERT INTO tasks VALUES('01a1cae0-ae3e-7e97-813b-875aafda8c4b',1,4,'default/67','01a1cae0-ab0a-711e-b9c1-2463a52d371e','Running','running','',1794405084734,1794405084736,NULL,'[]','[]','',1,NULL);
CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    display_conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    operation_key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    size INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
CREATE TABLE routine_notification_targets (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('telegram', 'feishu', 'wechat')),
    account_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    UNIQUE (agent_id, platform, account_id, peer)
) STRICT;
CREATE TABLE routine_notification_deliveries (
    run_id TEXT PRIMARY KEY REFERENCES automation_runs(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    account_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    body TEXT NOT NULL,
    rendered_parts TEXT NOT NULL DEFAULT '',
    parts INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'rejected')),
    reason TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE TABLE questions (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    source_submission_id TEXT NOT NULL,
    tool_operation_key TEXT NOT NULL UNIQUE,
    text TEXT NOT NULL,
    options_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('open', 'answered', 'consumed', 'dismissed')),
    answer_submission_id TEXT,
    created_at INTEGER NOT NULL,
    settled_at INTEGER
) STRICT;
CREATE TABLE task_steer_receipts (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL REFERENCES agents (id),
    parent_conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    task_id TEXT NOT NULL REFERENCES tasks (id),
    request_id TEXT NOT NULL,
    native_request_id TEXT NOT NULL UNIQUE,
    content TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'reconciliation', 'undelivered')),
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE (agent_id, request_id)
) STRICT;
CREATE TABLE IF NOT EXISTS "question_messages" (
    agent_id TEXT NOT NULL REFERENCES agents (id),
    peer TEXT NOT NULL,
    remote_id TEXT NOT NULL,
    question_id TEXT NOT NULL REFERENCES questions (id),
    PRIMARY KEY (agent_id, peer, remote_id)
) STRICT, WITHOUT ROWID;
CREATE TABLE one_password (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    mode TEXT NOT NULL CHECK (mode IN ('', 'connect', 'service-account')),
    connect_host TEXT NOT NULL,
    connect_token TEXT NOT NULL,
    service_account_token TEXT NOT NULL
) STRICT;
PRAGMA writable_schema=ON;
CREATE TABLE IF NOT EXISTS sqlite_sequence(name,seq);
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('goose_db_version',37);
CREATE TRIGGER routine_notification_target_identity BEFORE UPDATE ON routine_notification_targets
BEGIN SELECT RAISE(ABORT, 'notification targets are immutable'); END;
CREATE TRIGGER routine_notification_target_insert BEFORE INSERT ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
CREATE TRIGGER routine_notification_target_update BEFORE UPDATE OF notification_target_id, agent_id ON automations
WHEN NEW.notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.notification_target_id AND t.agent_id = NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
CREATE TRIGGER agent_notification_target_update BEFORE UPDATE OF primary_notification_target_id ON agents
WHEN NEW.primary_notification_target_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM routine_notification_targets t WHERE t.id = NEW.primary_notification_target_id AND t.agent_id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'notification target belongs to another agent'); END;
CREATE TRIGGER reject_revoked_notification_target BEFORE DELETE ON routine_notification_targets
BEGIN
    UPDATE routine_notification_deliveries SET status = 'rejected', reason = 'destination no longer authorized'
    WHERE target_id = OLD.id AND status = 'pending';
    UPDATE automations SET notification_route = 'none', notification_target_id = NULL
    WHERE agent_id = OLD.agent_id AND notification_target_id = OLD.id;
END;
CREATE TRIGGER revoke_deleted_telegram_targets AFTER DELETE ON telegram_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = OLD.agent_id AND platform = 'telegram'; END;
CREATE TRIGGER revoke_changed_telegram_targets AFTER UPDATE ON telegram_settings
BEGIN DELETE FROM routine_notification_targets
    WHERE agent_id = NEW.agent_id AND platform = 'telegram'
      AND (account_id <> substr(NEW.token, 1, instr(NEW.token, ':') - 1)
        OR instr(',' || replace(NEW.allowed_users, ' ', '') || ',', ',' || substr(peer, 10) || ',') = 0);
END;
CREATE TRIGGER revoke_deleted_feishu_targets AFTER DELETE ON feishu_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = OLD.agent_id AND platform = 'feishu'; END;
CREATE TRIGGER revoke_changed_feishu_targets AFTER UPDATE ON feishu_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = NEW.agent_id AND platform = 'feishu'
  AND (account_id <> NEW.app_id OR peer <> 'feishu:' || NEW.owner_id OR NEW.owner_id = ''); END;
CREATE TRIGGER revoke_deleted_wechat_targets AFTER DELETE ON wechat_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = OLD.agent_id AND platform = 'wechat'; END;
CREATE TRIGGER revoke_changed_wechat_targets AFTER UPDATE ON wechat_settings
BEGIN DELETE FROM routine_notification_targets WHERE agent_id = NEW.agent_id AND platform = 'wechat'
  AND (account_id <> NEW.bot_id OR peer <> 'wechat:' || NEW.owner OR NEW.owner = ''); END;
CREATE INDEX submissions_pending ON submissions (created_at) WHERE status = 'pending';
CREATE UNIQUE INDEX submissions_source_key ON submissions (source_key);
CREATE UNIQUE INDEX automation_runs_due ON automation_runs (automation_id, due_at);
CREATE INDEX automation_runs_recent ON automation_runs (automation_id, created_at);
CREATE INDEX submissions_conversation_kind ON submissions (conversation_id, kind);
CREATE INDEX submissions_channel_replies ON submissions (conversation_id, origin)
WHERE status != 'pending' AND delivered_at IS NULL
AND (kind = 'message' OR (kind = 'task_result' AND status = 'done'));
CREATE INDEX submissions_channel_pending ON submissions (origin, conversation_id)
WHERE status = 'pending';
CREATE INDEX channel_command_delivery ON channel_commands (agent_id, completed, delivered_at);
CREATE INDEX tasks_parent ON tasks (parent_conversation_id, created_at);
CREATE INDEX artifacts_by_conversation ON artifacts(agent_id, display_conversation_id, created_at);
CREATE UNIQUE INDEX questions_one_open ON questions (conversation_id) WHERE status = 'open';
CREATE UNIQUE INDEX questions_answer ON questions (answer_submission_id) WHERE answer_submission_id IS NOT NULL;
CREATE INDEX questions_source ON questions (source_submission_id);
CREATE INDEX task_steer_receipts_task ON task_steer_receipts (task_id, sequence);
PRAGMA writable_schema=OFF;
COMMIT;
