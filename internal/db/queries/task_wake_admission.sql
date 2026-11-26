-- name: SnapshotTaskWakeAdmission :one
INSERT INTO task_wake_admission_snapshots (submission_id, content, task_id)
SELECT s.id, s.content, i.task_id FROM submissions s
LEFT JOIN work_task_inputs i ON i.submission_id = s.id
WHERE s.id = @id AND s.kind = 'task_wake' AND s.status = 'pending' AND s.admitted_at IS NULL AND s.stop_requested = 0
ON CONFLICT (submission_id) DO UPDATE SET content = excluded.content, task_id = excluded.task_id
RETURNING content;

-- name: TakeTaskWakeAdmissionSnapshot :one
DELETE FROM task_wake_admission_snapshots WHERE submission_id = ? RETURNING content, task_id;

-- name: MarkTaskWakeAdmitted :exec
UPDATE submissions SET admitted_at = ?, content = ? WHERE id = ? AND admitted_at IS NULL;
