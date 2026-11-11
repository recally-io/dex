-- Child sessions replace tasks (ADR 0015). Nothing reads or writes the tasks table now. The
-- cleanup removes it.

-- name: GetConversationByExecution :one
SELECT * FROM conversations WHERE agent_id = ? AND execution_id = ?;

-- name: ListStopConversations :many
-- Stop reaches the conversation and the children of it that have pending input that the parent
-- started, whatever bot the child belongs to. A child with only user input is not reached.
SELECT * FROM conversations
WHERE (conversations.id = @chat_id AND conversations.agent_id = @agent_id)
OR (conversations.parent_id = @chat_id AND EXISTS (
    SELECT 1 FROM submissions s WHERE s.conversation_id = conversations.id AND s.status = 'pending'
        AND s.parent_request_id IS NOT NULL
))
ORDER BY conversations.id;

-- name: StopConversationSubmissions :execrows
-- A child that Stop reaches through its parent keeps its pending user input. Stop on the child
-- itself ends all of it.
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE conversation_id = @chat_id AND status = 'pending'
    AND (CAST(@parent_only AS INTEGER) = 0 OR parent_request_id IS NOT NULL);
