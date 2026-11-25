-- name: AllocateWorkTaskNumber :one
INSERT INTO work_task_numbers DEFAULT VALUES RETURNING number;

-- name: GetActiveWorkTaskAssignee :one
SELECT id FROM agents WHERE id = ? AND archived_at IS NULL;

-- name: CreateWorkTask :one
INSERT INTO work_tasks (id, assignee_bot_id, title, created_at, updated_at, number, status,
  creator_kind, creator_id, authorized_by_root_id, reviewer_kind, reviewer_id, parent_task_id)
VALUES (@id, @agent_id, @title, @at, @at, @number, @status,
  @creator_kind, @creator_id, sqlc.narg(authorized_by_root_id), @reviewer_kind, @reviewer_id, sqlc.narg(parent_task_id))
RETURNING *;

-- name: ListWorkTasks :many
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE a.archived_at IS NULL AND (t.status NOT IN ('done', 'cancelled') OR sqlc.arg(include_closed))
ORDER BY t.updated_at DESC, t.id DESC;

-- name: GetWorkTask :one
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE t.id = @id AND t.assignee_bot_id = @agent_id AND a.archived_at IS NULL;

-- name: GetBoardTask :one
SELECT t.* FROM work_tasks t JOIN agents a ON a.id = t.assignee_bot_id
WHERE t.id = ? AND a.archived_at IS NULL;

-- name: LockWorkTaskWriter :exec
-- Take the SQLite writer before reading state to avoid promoting a stale read snapshot.
UPDATE work_tasks SET revision = revision WHERE 0;

-- name: AdvanceWorkTask :one
UPDATE work_tasks SET next_entry_seq = next_entry_seq + 1, revision = revision + 1,
  updated_at = max(updated_at, CAST(@at AS INTEGER)), status = @status, review_seq = @review_seq, title = @title
WHERE id = @id AND revision = @revision
RETURNING *;

-- name: InsertWorkTaskEntry :one
INSERT INTO work_task_entries (task_id, seq, kind, tag, actor_kind, actor_bot_id, actor_execution_id,
  trust, event_id, meta_json, content, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetWorkTaskEntry :one
SELECT * FROM work_task_entries WHERE task_id = ? AND seq = ?;

-- name: IsWorkTaskEntryRetracted :one
SELECT EXISTS (SELECT 1 FROM work_task_entries WHERE task_id = @task_id AND kind = 'tombstone'
  AND CAST(json_extract(meta_json, '$.targetSeq') AS INTEGER) = CAST(@target_seq AS INTEGER));

-- name: ListWorkTaskEntries :many
SELECT e.* FROM work_task_entries e WHERE e.task_id = @task_id AND e.kind IN ('note', 'artifact', 'external')
AND json_extract(e.meta_json, '$.legacyHidden') IS NOT 1
AND NOT EXISTS (SELECT 1 FROM work_task_entries r WHERE r.task_id = e.task_id AND r.kind = 'tombstone'
  AND CAST(json_extract(r.meta_json, '$.targetSeq') AS INTEGER) = e.seq)
ORDER BY e.id DESC LIMIT 100;

-- name: AssociateWorkTaskInput :exec
INSERT INTO work_task_inputs (submission_id, task_id) VALUES (?, ?);

-- name: WorkTaskForInput :one
SELECT task_id FROM work_task_inputs WHERE submission_id = ?;

-- name: ListWorkTaskQuestions :many
SELECT q.* FROM questions q
JOIN work_task_inputs i ON i.submission_id = q.source_submission_id
JOIN submissions s ON s.id = q.source_submission_id
WHERE i.task_id = @task_id AND q.status = 'open' AND s.status = 'done'
ORDER BY q.rowid;

-- name: WorkTaskForQuestion :one
SELECT i.task_id FROM work_task_inputs i JOIN questions q ON q.source_submission_id = i.submission_id
WHERE q.id = @question_id AND q.conversation_id = @conversation_id;

-- name: ListWorkTaskQuestionBlockers :many
SELECT q.id, q.text, CAST(CASE WHEN c.channel_key = 'main' THEN 1 + (
  SELECT count(*) FROM questions prior JOIN submissions ps ON ps.id = prior.source_submission_id
  WHERE prior.status = 'open' AND ps.status = 'done' AND prior.conversation_id = q.conversation_id
    AND (prior.queued, prior.rowid) < (q.queued, q.rowid)
) ELSE 0 END AS INTEGER) AS queue_position
FROM questions q JOIN work_task_inputs i ON i.submission_id = q.source_submission_id
JOIN submissions s ON s.id = q.source_submission_id JOIN conversations c ON c.id = q.conversation_id
WHERE i.task_id = @task_id AND q.status = 'open' AND s.status = 'done'
ORDER BY q.queued, q.rowid;
