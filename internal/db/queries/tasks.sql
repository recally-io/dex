-- Child sessions replace tasks (ADR 0015). Nothing reads or writes the tasks table now. The
-- cleanup removes it.

-- name: GetConversationByExecution :one
SELECT * FROM conversations WHERE agent_id = ? AND execution_id = ?;

-- name: ListStopConversations :many
-- Stop reaches the conversation and the children of it that have pending input that the parent
-- started. A child with only user input is not reached.
SELECT * FROM conversations WHERE conversations.agent_id = @agent_id
AND (conversations.id = @chat_id OR (conversations.parent_id = @chat_id AND EXISTS (
    SELECT 1 FROM submissions s WHERE s.conversation_id = conversations.id AND s.status = 'pending'
        AND s.parent_request_id IS NOT NULL
)))
ORDER BY conversations.id;

-- name: StopConversationSubmissions :execrows
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE conversation_id = @chat_id AND status = 'pending';

-- name: StopSubmission :execrows
-- Stop ends one pending input that native execution withdrew.
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE id = @id AND status = 'pending';
