-- Stop of a conversation and of its child sessions (ADR 0015). The file keeps its name from the
-- task layer that child sessions replaced.

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

-- name: ListChildrenStoppedByParent :many
-- The children that Stop on their parent reaches. Keep the rule equal to ListStopConversations: the
-- web UI shows Stop on a parent page only for these.
SELECT conversations.id FROM conversations
JOIN conversations parent ON parent.id = conversations.parent_id
WHERE (conversations.agent_id = @agent_id OR parent.agent_id = @agent_id) AND EXISTS (
    SELECT 1 FROM submissions s WHERE s.conversation_id = conversations.id AND s.status = 'pending'
        AND s.parent_request_id IS NOT NULL
);

-- name: StopConversationSubmissions :execrows
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE conversation_id = @chat_id AND status = 'pending';

-- name: StopSubmission :execrows
-- Stop ends one pending input that native execution withdrew.
UPDATE submissions SET status = 'unanswered', reason = 'stopped', settled_at = @now
WHERE id = @id AND status = 'pending';
