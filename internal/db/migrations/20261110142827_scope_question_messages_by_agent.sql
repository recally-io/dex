-- +goose Up
-- Remote message IDs can be equal in the private chats of different bots.
CREATE TABLE question_messages_scoped (
    agent_id TEXT NOT NULL REFERENCES agents (id),
    peer TEXT NOT NULL,
    remote_id TEXT NOT NULL,
    question_id TEXT NOT NULL REFERENCES questions (id),
    PRIMARY KEY (agent_id, peer, remote_id)
) STRICT, WITHOUT ROWID;

INSERT INTO question_messages_scoped (agent_id, peer, remote_id, question_id)
SELECT q.agent_id, m.peer, m.remote_id, m.question_id
FROM question_messages m JOIN questions q ON q.id = m.question_id;

DROP TABLE question_messages;
ALTER TABLE question_messages_scoped RENAME TO question_messages;

-- +goose Down
CREATE TABLE question_messages_unscoped (
    peer TEXT NOT NULL,
    remote_id TEXT NOT NULL,
    question_id TEXT NOT NULL REFERENCES questions (id),
    PRIMARY KEY (peer, remote_id)
) STRICT, WITHOUT ROWID;

-- The old schema can keep only one question for each remote message ID.
INSERT INTO question_messages_unscoped (peer, remote_id, question_id)
SELECT peer, remote_id, MIN(question_id)
FROM question_messages GROUP BY peer, remote_id;

DROP TABLE question_messages;
ALTER TABLE question_messages_unscoped RENAME TO question_messages;
