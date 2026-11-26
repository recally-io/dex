-- name: GetQuestionByOperation :one
SELECT * FROM questions WHERE tool_operation_key = ?;

-- name: CreateQuestion :exec
INSERT INTO questions (id, bot_id, session_id, source_submission_id, tool_operation_key, text, options_json, status, created_at,
    action, action_bot_id, approve_text, queued)
VALUES (@id, @bot_id, @session_id, @source_submission_id, @tool_operation_key, @text, @options_json, 'open', @created_at,
    @action, @action_bot_id, @approve_text, @queued);

-- name: GetCurrentQuestion :one
-- The question that waits for the user, or whose answer waits in the queue.
SELECT * FROM questions WHERE session_id = ? AND queued = 0 AND status IN ('open', 'answered')
ORDER BY rowid DESC LIMIT 1;

-- name: ListCurrentQuestions :many
SELECT * FROM questions WHERE bot_id = ? AND queued = 0 AND status IN ('open', 'answered');

-- name: PromoteQueuedQuestion :one
UPDATE questions SET queued = 0
WHERE id = (
  SELECT q.id FROM questions q JOIN submissions s ON s.id = q.source_submission_id
  WHERE q.session_id = @session_id AND q.queued = 1 AND q.status = 'open' AND s.status = 'done'
  ORDER BY q.rowid LIMIT 1
) AND NOT EXISTS (
  SELECT 1 FROM questions current WHERE current.session_id = @session_id
  AND current.queued = 0 AND current.status IN ('open', 'answered')
)
RETURNING *;

-- name: GetQuestion :one
SELECT * FROM questions WHERE id = ?;

-- name: GetQuestionByAnswer :one
SELECT * FROM questions WHERE answer_submission_id = ?;

-- name: GetQuestionBySource :one
SELECT * FROM questions WHERE source_submission_id = ? ORDER BY rowid LIMIT 1;

-- name: AnswerQuestion :execrows
-- Only an open question of this bot and session accepts an answer. The outcome is the
-- decision of an action question; control decides it in the same transaction.
UPDATE questions SET status = 'answered', answer_submission_id = @submission_id, outcome = @outcome
WHERE id = @id AND bot_id = @bot_id AND session_id = @session_id AND status = 'open';

-- name: ConsumeQuestion :exec
UPDATE questions SET status = 'consumed', settled_at = @now
WHERE answer_submission_id = @submission_id AND status = 'answered';

-- name: ReopenQuestion :execrows
-- The withdrawn answer ID stays, so the withdrawn text can drop the question wrapper again.
-- A decided action question never opens again: its action ran or was refused for good, so it
-- closes instead.
UPDATE questions SET
    status = CASE WHEN outcome IS NULL THEN 'open' ELSE 'consumed' END,
    settled_at = CASE WHEN outcome IS NULL THEN settled_at ELSE CAST(unixepoch('subsec') * 1000 AS INTEGER) END
WHERE answer_submission_id = @submission_id AND status = 'answered';

-- name: DismissQuestions :exec
-- Stop, Clear and archive close the question and an answer that never ran.
UPDATE questions SET status = 'dismissed', settled_at = @now
WHERE session_id = @session_id AND status IN ('open', 'answered');

-- name: AddQuestionMessage :exec
INSERT OR IGNORE INTO question_messages (bot_id, peer, remote_id, question_id) VALUES (?, ?, ?, ?);

-- name: GetQuestionByMessage :one
SELECT question_id FROM question_messages WHERE bot_id = ? AND peer = ? AND remote_id = ?;
