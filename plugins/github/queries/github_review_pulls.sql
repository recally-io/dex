-- name: SaveGitHubReviewPull :exec
INSERT INTO plugin_github_review_pulls (repository_id, pull_number, token, configuration_id) VALUES (?, ?, ?, ?)
ON CONFLICT (repository_id, pull_number) DO UPDATE SET
  token = excluded.token, configuration_id = excluded.configuration_id, etag = '';

-- name: DeleteGitHubReviewPull :exec
DELETE FROM plugin_github_review_pulls WHERE repository_id = ? AND pull_number = ?;

-- name: ListGitHubReviewPulls :many
SELECT * FROM plugin_github_review_pulls ORDER BY repository_id, pull_number;

-- name: AdvanceGitHubReviewPull :exec
UPDATE plugin_github_review_pulls SET etag = @next_etag
WHERE repository_id = @repository_id AND pull_number = @pull_number
  AND configuration_id = @configuration_id AND etag = @previous_etag;
