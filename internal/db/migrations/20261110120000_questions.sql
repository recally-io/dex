-- +goose Up
-- A question that the agent asks the user through ask_user. The partial unique index lets a
-- conversation hold one open question; a replayed tool call finds its row by tool_operation_key.
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

CREATE UNIQUE INDEX questions_one_open ON questions (conversation_id) WHERE status = 'open';
CREATE UNIQUE INDEX questions_answer ON questions (answer_submission_id) WHERE answer_submission_id IS NOT NULL;
CREATE INDEX questions_source ON questions (source_submission_id);

-- +goose Down
DROP INDEX questions_source;
DROP INDEX questions_answer;
DROP INDEX questions_one_open;
DROP TABLE questions;
