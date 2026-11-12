-- name: CreateChildConversation :one
-- A child session is a Chat with a parent. Its first turn is stored in the same transaction.
INSERT INTO conversations (agent_id, channel_key, title, model_provider, model_id, thinking_level, cwd, parent_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CreateParentTurn :execrows
-- A turn that the parent session sends. The origin task keeps the turn out of user channels
-- and routine creation. A turn whose ID exists already is not stored again.
INSERT INTO submissions (id, conversation_id, content, attachments, origin, kind, when_busy, parent_request_id, status, created_at)
VALUES (@id, @conversation_id, @content, 'null', 'task', 'message', @when_busy, @parent_request_id, 'pending', @created_at)
ON CONFLICT DO NOTHING;

-- name: ListSessions :many
-- The children of the caller, of any bot, and with all_sessions the sessions of one bot or of every
-- bot, newest activity first. A row names the parent. The list leaves out dreams.
SELECT
    c.id,
    c.agent_id,
    a.name AS agent_name,
    c.title,
    c.model_provider,
    c.model_id,
    c.parent_id,
    CAST(max(
        c.created_at,
        coalesce((SELECT max(s.created_at) FROM submissions s WHERE s.conversation_id = c.id), c.created_at),
        coalesce((SELECT max(s.settled_at) FROM submissions s WHERE s.conversation_id = c.id), c.created_at)
    ) AS INTEGER) AS updated_at,
    CAST((SELECT count(*) FROM submissions s WHERE s.conversation_id = c.id AND s.status = 'pending') AS INTEGER) AS pending
FROM conversations c
JOIN agents a ON a.id = c.agent_id
WHERE c.channel_key NOT LIKE 'dream:%'
    AND (c.parent_id = @parent_id OR (
        CAST(@all_sessions AS INTEGER) = 1
        AND (CAST(@all_bots AS INTEGER) = 1 OR c.agent_id = @agent_id)
    ))
ORDER BY updated_at DESC, c.id DESC
LIMIT @max_rows;

-- name: PendingCount :one
SELECT CAST(count(*) AS INTEGER) FROM submissions WHERE conversation_id = ? AND status = 'pending';

-- name: LatestTurn :one
SELECT s.id, s.origin, s.status, s.reason, s.answer, s.created_at, s.settled_at
FROM submissions s
WHERE s.conversation_id = ? AND s.kind = 'message'
ORDER BY s.rowid DESC LIMIT 1;

-- name: GetTurn :one
-- A turn is an input of a person or a session. The wake-up of a background command is a turn too: a
-- parent reads it when the result of a child session cut its answer. It stays a non-user input,
-- and ListTurns does not list it.
SELECT s.id, s.conversation_id, s.content, s.origin, s.status, s.reason, s.answer, s.created_at, s.settled_at
FROM submissions s
WHERE s.id = ? AND (s.kind = 'message' OR (s.kind = 'task_result' AND s.id LIKE 'background:%'));

-- name: ListTurns :many
-- The turns of a session, newest first. A non-empty before is the turn ID that the page follows.
SELECT s.id, s.content, s.origin, s.status, s.reason, s.answer, s.created_at, s.settled_at
FROM submissions s
WHERE s.conversation_id = @conversation_id AND s.kind = 'message'
    AND (CAST(@before AS TEXT) = '' OR s.rowid < (SELECT x.rowid FROM submissions x WHERE x.id = @before))
ORDER BY s.rowid DESC
LIMIT @max_rows;

-- name: SearchTurns :many
-- A turn matches when every non-empty word is a substring of its input or its answer. The caller
-- lowercases ASCII letters of the words, and lower() does the same for the text.
SELECT s.id, s.conversation_id, c.agent_id, a.name AS agent_name, c.title, s.content, s.answer, s.created_at
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
JOIN agents a ON a.id = c.agent_id
WHERE s.kind = 'message' AND c.channel_key NOT LIKE 'dream:%'
    AND (CAST(@all_bots AS INTEGER) = 1 OR c.agent_id = @agent_id)
    AND (CAST(@session_id AS INTEGER) = 0 OR c.id = @session_id)
    AND s.created_at >= @since
    AND (CAST(@w1 AS TEXT) = '' OR instr(lower(s.content), CAST(@w1 AS TEXT)) > 0 OR instr(lower(coalesce(s.answer, '')), CAST(@w1 AS TEXT)) > 0)
    AND (CAST(@w2 AS TEXT) = '' OR instr(lower(s.content), CAST(@w2 AS TEXT)) > 0 OR instr(lower(coalesce(s.answer, '')), CAST(@w2 AS TEXT)) > 0)
    AND (CAST(@w3 AS TEXT) = '' OR instr(lower(s.content), CAST(@w3 AS TEXT)) > 0 OR instr(lower(coalesce(s.answer, '')), CAST(@w3 AS TEXT)) > 0)
    AND (CAST(@w4 AS TEXT) = '' OR instr(lower(s.content), CAST(@w4 AS TEXT)) > 0 OR instr(lower(coalesce(s.answer, '')), CAST(@w4 AS TEXT)) > 0)
    AND (CAST(@w5 AS TEXT) = '' OR instr(lower(s.content), CAST(@w5 AS TEXT)) > 0 OR instr(lower(coalesce(s.answer, '')), CAST(@w5 AS TEXT)) > 0)
    AND (CAST(@w6 AS TEXT) = '' OR instr(lower(s.content), CAST(@w6 AS TEXT)) > 0 OR instr(lower(coalesce(s.answer, '')), CAST(@w6 AS TEXT)) > 0)
ORDER BY s.rowid DESC
LIMIT @max_rows;

-- name: GetTurnParent :one
-- The parent link of a settled turn and what its result needs. No row means that the turn is not
-- a parent turn.
SELECT s.parent_request_id, s.created_at, c.id AS child_id, c.title, c.parent_id
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.id = ? AND s.parent_request_id IS NOT NULL AND c.parent_id IS NOT NULL;

-- name: CreateTurnResult :execrows
-- The result of a parent turn goes to the parent session with the origin of the parent request.
-- The ID follows the turn, so a replay does not store a second result.
INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at, result_child_id, native_answer_id)
SELECT @id, c.parent_id, @content, 'null', @id, source.origin, 'task_result', 'pending', @created_at, c.id, sqlc.narg(native_answer_id)
FROM submissions turn
JOIN conversations c ON c.id = turn.conversation_id
JOIN submissions source ON source.id = turn.parent_request_id
WHERE turn.id = @turn_id AND c.parent_id IS NOT NULL
ON CONFLICT (result_child_id, native_answer_id) DO NOTHING;

-- name: ListRecoveryChildren :many
-- The child sessions with a pending parent turn. Recovery reads the native state of each pending
-- input, because the admission receipt and the native admission are not one transaction.
SELECT DISTINCT c.id, c.agent_id, c.execution_id
FROM submissions s
JOIN conversations c ON c.id = s.conversation_id
WHERE s.status = 'pending' AND s.parent_request_id IS NOT NULL
    AND c.parent_id IS NOT NULL AND c.execution_id IS NOT NULL
ORDER BY c.id;

-- name: ListChildPending :many
SELECT id, parent_request_id IS NOT NULL AS from_parent
FROM submissions
WHERE conversation_id = ? AND status = 'pending'
ORDER BY rowid;
