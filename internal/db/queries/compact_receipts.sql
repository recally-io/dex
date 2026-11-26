-- name: GetCompactReceipt :one
SELECT * FROM compact_receipts WHERE id = ?;

-- name: CreateCompactReceipt :execrows
INSERT INTO compact_receipts (id, session_id, instructions)
VALUES (?, ?, ?)
ON CONFLICT DO NOTHING;
