-- name: IssueRunnerCode :execrows
INSERT INTO runner_pairing (runner_id, code_digest, expires_at)
SELECT id, @code_digest, @expires_at FROM runners WHERE id = @runner_id AND kind = 'runner'
ON CONFLICT (runner_id) DO UPDATE SET code_digest = excluded.code_digest, expires_at = excluded.expires_at
WHERE runner_pairing.credential_digest = '';

-- name: RedeemRunnerCode :execrows
UPDATE runner_pairing SET code_digest = '', expires_at = 0, credential_digest = @credential_digest
WHERE runner_id = @runner_id AND code_digest = @code_digest AND expires_at > @now AND credential_digest = '';

-- name: GetRunnerCredential :one
SELECT credential_digest FROM runner_pairing WHERE runner_id = ?;

-- name: RevokeRunnerCredential :exec
DELETE FROM runner_pairing WHERE runner_id = ?;
