-- Legacy task rows. Child sessions replace tasks (ADR 0015). Nothing writes these rows now.
-- The web UI of the previous release still reads them until the cleanup removes the table.

-- name: GetConversationByExecution :one
SELECT * FROM conversations WHERE agent_id = ? AND execution_id = ?;

-- name: ListAgentTasks :many
SELECT t.id, t.parent_conversation_id, t.conversation_id, c.execution_id, t.title, t.status,
    t.created_at, t.started_at, t.ended_at, t.handled_at, t.structured_report
FROM tasks t JOIN conversations c ON c.id = t.conversation_id
WHERE c.agent_id = ? ORDER BY t.created_at DESC, t.id DESC;

-- name: AcknowledgeTask :execrows
UPDATE tasks SET handled_at = coalesce(handled_at, @handled_at)
WHERE tasks.id = @task_id AND tasks.status IN ('done', 'failed', 'cancelled', 'interrupted')
AND tasks.conversation_id IN (SELECT c.id FROM conversations c WHERE c.agent_id = @agent_id);

-- name: RestoreTask :execrows
UPDATE tasks SET handled_at = NULL
WHERE tasks.id = @task_id AND tasks.status IN ('done', 'failed', 'cancelled', 'interrupted')
    AND tasks.conversation_id IN (SELECT c.id FROM conversations c WHERE c.agent_id = @agent_id);

-- name: GetAgentTask :one
SELECT t.* FROM tasks t JOIN conversations c ON c.id = t.conversation_id
WHERE t.id = ? AND c.agent_id = ?;

-- name: ListStopConversations :many
-- Stop reaches the conversation and the children of it that have pending input.
SELECT * FROM conversations WHERE conversations.agent_id = @agent_id
AND (conversations.id = @chat_id OR (conversations.parent_id = @chat_id AND EXISTS (
    SELECT 1 FROM submissions s WHERE s.conversation_id = conversations.id AND s.status = 'pending'
)))
ORDER BY conversations.id;

-- name: StopConversationSubmissions :execrows
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE conversation_id = @chat_id AND status = 'pending';
