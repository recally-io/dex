-- +goose Up
CREATE TABLE agent_instruction_import (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    pending_files TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE agent_instruction_import;
