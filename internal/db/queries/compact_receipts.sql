-- name: GetCompactReceipt :one
SELECT * FROM compact_receipts WHERE id = ?;

-- name: CreateCompactReceipt :execrows
INSERT INTO compact_receipts (id, conversation_id, instructions)
VALUES (?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: DeleteCompactReceipt :exec
DELETE FROM compact_receipts WHERE id = ?;
