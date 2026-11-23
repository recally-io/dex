-- name: ListAgentIDsForInstructionImport :many
SELECT id FROM agents ORDER BY id;

-- name: AppendAgentInstructions :exec
UPDATE agents SET instructions = CASE
    WHEN instructions = '' THEN @text
    ELSE instructions || char(10, 10) || '---' || char(10, 10) || @text
END WHERE id = @id;
