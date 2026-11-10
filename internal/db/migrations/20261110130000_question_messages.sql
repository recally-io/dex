-- +goose Up
-- The remote message that carries a question, so a quoted reply on a channel can answer it. A reply
-- can have several parts, and a part sent twice after a crash has two remote IDs.
CREATE TABLE question_messages (
    peer TEXT NOT NULL,
    remote_id TEXT NOT NULL,
    question_id TEXT NOT NULL REFERENCES questions (id),
    PRIMARY KEY (peer, remote_id)
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE question_messages;
