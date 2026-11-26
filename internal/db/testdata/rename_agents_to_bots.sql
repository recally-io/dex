INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES
    ('fixture', 'Fixture', '', '', 1), ('target', 'Target', '', '', 1);
INSERT INTO sessions (id, agent_id, channel_key, execution_id, created_at) VALUES (1, 'fixture', 'main', 10, 1);
INSERT INTO submissions (id, session_id, content, status, created_at) VALUES ('input', 1, 'Input', 'done', 1);
INSERT INTO dreams (id, agent_id, through_settled_at, through_id, created_at) VALUES ('dream', 'fixture', 1, 'input', 1);
INSERT INTO automations (id, agent_id, name, prompt, schedule, time_zone, enabled, created_at, updated_at, session)
    VALUES ('auto', 'fixture', 'Watch', 'Check', '{}', 'UTC', 1, 1, 1, 'reuse');
INSERT INTO automation_tool_operations (key, agent_id, session_id, source_request_id, action, result)
    VALUES ('aop', 'fixture', 1, 'input', 'create', '{}');
INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('fixture', '123:fixture', '42');
INSERT INTO wechat_settings (agent_id, token, base_url, bot_id, owner)
    VALUES ('fixture', 'token', 'https://example.invalid', 'wx-bot', 'wx-owner');
INSERT INTO feishu_settings (agent_id, app_id, app_secret, brand, owner_id)
    VALUES ('fixture', 'fs-app', 'secret', 'feishu', 'fs-owner');
INSERT INTO channel_commands (agent_id, source_key, peer, channel_key, command)
    VALUES ('fixture', 'command', 'telegram:42', 'main', '/help');
INSERT INTO automation_notification_targets (id, agent_id, platform, account_id, peer)
    VALUES ('notice-target', 'fixture', 'telegram', '123', 'telegram:42');
UPDATE agents SET primary_notification_target_id = 'notice-target' WHERE id = 'fixture';
INSERT INTO questions (id, agent_id, session_id, source_submission_id, tool_operation_key, text, options_json, status, created_at, action, action_agent_id)
    VALUES ('question', 'fixture', 1, 'input', 'qop', 'Continue?', '[]', 'open', 1, 'archive_bot', 'target');
INSERT INTO question_messages (agent_id, peer, remote_id, question_id) VALUES ('fixture', 'telegram:42', '17', 'question');
INSERT INTO telegram_album_members (source_key, agent_id, account_id, sender_id, peer, group_key, input_json, received_at)
    VALUES ('album', 'fixture', '123', '42', 'telegram:42', 'group', '{}', 1);
INSERT INTO telegram_effects (operation_key, agent_id, account_id, sender_id, peer, source_key, kind, state, created_at)
    VALUES ('effect', 'fixture', '123', '42', 'telegram:42', 'input', 'reaction', 'ready', 1);
INSERT INTO monitors (id, operation_key, agent_id, session_id, root_request_id, source_submission_id, chain_depth, due_at, prompt, created_at, updated_at)
    VALUES ('monitor', 'mop', 'fixture', 1, 'input', 'input', 0, 2, 'Check', 1, 1);
INSERT INTO task_tool_operations (key, agent_id, session_id, source_request_id, action, result)
    VALUES ('top', 'fixture', 1, 'input', 'create', '{}');
INSERT INTO mcp_servers (agent_id, name, config, position, updated_at) VALUES ('fixture', 'server', '{}', 0, 1);
INSERT INTO telegram_managed_setups (id, agent_id, manager_id, username, expires_at, bot_id)
    VALUES ('setup', 'fixture', 555, 'fixture_bot', 2, 777);
INSERT INTO channel_webhook_queue (id, platform, account, event_id, agent_id, received_at, payload)
    VALUES (1, 'telegram', '123', 'hook', 'fixture', 1, X'7B7D');
INSERT INTO web_notifications (id, event_id, agent_id, session_id, source_submission_id, body, created_at)
    VALUES ('notice', 'notice:event', 'fixture', 1, 'input', 'Saved notice', 1);
INSERT INTO telegram_callbacks (id, agent_id, source_id, ordinal, peer, channel_context, session_id, execution_id, context_revision, label, object_id, action, expires_at)
    VALUES ('cb', 'fixture', 'input', 0, 'telegram:42', '{"accountId":"123","senderId":"42"}', 1, 10, '1', 'Open', 'question', 'open', 2);
INSERT INTO outbox_messages (id, event_id, agent_id, session_id, source_submission_id, kind, peer, channel_context, body, created_at)
    VALUES ('reply', 'reply:event', 'fixture', 1, 'input', 'reply', 'telegram:42', '{"accountId":"123"}', 'Saved reply', 1);
INSERT INTO outbox_files (id, agent_id, operation_key, outbox_id, name, mime_type, body)
    VALUES ('file', 'fixture', 'file:op', 'reply', 'file.txt', 'text/plain', X'61');
INSERT INTO tasks (id, assignee_bot_id, title, created_at, updated_at) VALUES ('task', 'fixture', 'Task', 1, 1);
INSERT INTO task_sessions (id, task_id, bot_id, session_id, started_at) VALUES ('link', 'task', 'fixture', 1, 1);
INSERT INTO task_external_runs (id, task_id, bot_id, harness, started_at) VALUES ('run', 'task', 'fixture', 'codex', 1);
