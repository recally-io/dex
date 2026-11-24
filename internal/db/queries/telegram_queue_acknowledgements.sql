-- name: ClaimTelegramQueueAcknowledgement :execrows
INSERT INTO telegram_queue_acknowledgements (submission_id, outcome, updated_at)
SELECT id, 'unknown', sqlc.arg(updated_at) FROM submissions
WHERE id = sqlc.arg(submission_id) AND status = 'pending' AND stop_requested = 0
ON CONFLICT (submission_id) DO NOTHING;

-- name: RecordTelegramQueueAcknowledgement :exec
UPDATE telegram_queue_acknowledgements
SET outcome = ?, updated_at = ? WHERE submission_id = ?;

-- name: TelegramPrivateReactionsEnabled :one
SELECT EXISTS (
    SELECT 1 FROM telegram_settings WHERE agent_id = ?
    AND json_extract(options, '$.reactions') IS NOT 0
);
