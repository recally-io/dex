-- name: CreateBackgroundCommand :execrows
-- The row exists before the process, in state starting. A call key that exists already stores
-- nothing, and the caller reads the stored row.
INSERT INTO background_commands (id, call_key, conversation_id, source_request_id, wake, label, command, cwd, output_path, timeout_ms, pid, pgid, process_started_at, state)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, 'starting')
ON CONFLICT DO NOTHING;

-- name: MarkBackgroundCommandRunning :execrows
UPDATE background_commands SET state = 'running', pid = @pid, pgid = @pgid, process_started_at = @started_at
WHERE id = @id AND state = 'starting';

-- name: DeleteBackgroundCommand :exec
-- A command that never started leaves no row, so a replayed call can try again.
DELETE FROM background_commands WHERE id = ? AND state = 'starting';

-- name: GetBackgroundCommand :one
SELECT * FROM background_commands WHERE id = ?;

-- name: GetBackgroundCommandByKey :one
SELECT * FROM background_commands WHERE call_key = ?;

-- name: ListBackgroundCommands :many
SELECT * FROM background_commands WHERE conversation_id = ? ORDER BY process_started_at DESC, rowid DESC;

-- name: ListRunningBackgroundIDs :many
SELECT id FROM background_commands WHERE conversation_id = ? AND state IN ('starting', 'running');

-- name: ListRunningBackgroundIDsByAgent :many
SELECT b.id FROM background_commands b
JOIN conversations c ON c.id = b.conversation_id
WHERE c.agent_id = ? AND b.state IN ('starting', 'running');

-- name: CountRunningBackgroundCommands :one
SELECT count(*) FROM background_commands b
JOIN conversations c ON c.id = b.conversation_id
WHERE c.agent_id = ? AND b.state IN ('starting', 'running');

-- name: FinishBackgroundCommand :execrows
UPDATE background_commands SET state = @state, exit_status = @exit_status, reason = @reason, ended_at = @ended_at
WHERE id = @id AND state = 'running';

-- name: MarkBackgroundCommandsUnknown :execrows
UPDATE background_commands SET state = 'unknown' WHERE state IN ('starting', 'running');

-- name: CreateBackgroundWake :execrows
-- A wake-up is a durable input of the owning session, like the result of a child session. It keeps
-- the origin and the parent link of the input that started the command, so a reply reaches the
-- same channel and a child session still reports to its parent. The ID follows the event, so a
-- replay does not store a second input. An archived bot takes no input: the condition is part of
-- the insert, so an archive cannot slip between a check and the write.
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at, parent_request_id)
SELECT @id, b.conversation_id, @content, 'null', @id, s.origin, 'task_result', 'pending', @created_at, s.parent_request_id
FROM background_commands b
JOIN submissions s ON s.id = b.source_request_id
JOIN conversations c ON c.id = b.conversation_id
JOIN agents a ON a.id = c.agent_id
WHERE b.id = @background_id AND a.archived_at IS NULL
ON CONFLICT DO NOTHING;
