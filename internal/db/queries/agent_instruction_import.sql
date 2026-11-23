-- name: GetAgentInstructionImport :one
SELECT pending_files FROM agent_instruction_import WHERE id = 1;

-- name: RecordAgentInstructionImport :exec
INSERT INTO agent_instruction_import (id, pending_files) VALUES (1, ?);

-- name: FinishAgentInstructionImport :exec
UPDATE agent_instruction_import SET pending_files = '[]' WHERE id = 1;

-- name: ListAgentIDsForInstructionImport :many
SELECT id FROM agents ORDER BY id;

-- name: AppendAgentInstructions :exec
UPDATE agents SET instructions = CASE
    WHEN instructions = '' THEN @text
    ELSE instructions || char(10, 10) || '---' || char(10, 10) || @text
END WHERE id = @id;
