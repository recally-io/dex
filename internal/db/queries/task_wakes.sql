-- name: EnsureTaskMain :exec
INSERT INTO sessions (agent_id, channel_key, title, created_at)
SELECT a.id, 'main', a.name, @created_at FROM agents a WHERE a.id = @agent_id AND a.archived_at IS NULL
ON CONFLICT (agent_id, channel_key) DO NOTHING;

-- name: GetTaskMain :one
SELECT c.id FROM sessions c JOIN agents a ON a.id = c.agent_id
WHERE c.agent_id = ? AND c.channel_key = 'main' AND c.archived_at IS NULL AND a.archived_at IS NULL;

-- name: GetUnadmittedTaskWake :one
SELECT * FROM submissions WHERE session_id = ? AND kind = 'task_wake' AND status = 'pending' AND admitted_at IS NULL;

-- name: CreateTaskWake :exec
INSERT INTO submissions (id, session_id, content, attachments, source_key, origin, producer, reply_channel, reply_peer, kind, status, created_at, channel_context)
VALUES (@id, @session_id, @content, 'null', @id, 'web', 'plugin', '', '', 'task_wake', 'pending', @created_at, '{}');

-- name: UpdateUnadmittedTaskWake :exec
UPDATE submissions SET content = ? WHERE id = ? AND kind = 'task_wake' AND status = 'pending' AND admitted_at IS NULL;

-- name: ClearTaskWakeAssociation :exec
DELETE FROM task_inputs WHERE submission_id = ?;

-- name: CopyTaskWakeAssociation :exec
INSERT INTO task_inputs (submission_id, task_id)
SELECT @new_submission_id, i.task_id FROM task_inputs i WHERE i.submission_id = @source_submission_id;
