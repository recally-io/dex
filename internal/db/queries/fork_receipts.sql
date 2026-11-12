-- name: GetForkReceipt :one
SELECT * FROM fork_receipts WHERE id = ?;

-- name: CreateForkReceipt :execrows
INSERT INTO fork_receipts (id, source_id, entry_id, before_entry, fork_id)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;
