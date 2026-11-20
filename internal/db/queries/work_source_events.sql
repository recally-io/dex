-- name: ClaimWorkSourceReceipt :execrows
INSERT INTO work_source_receipts (namespace, receipt_key, event_id) VALUES (?, ?, ?)
ON CONFLICT (namespace, receipt_key) DO NOTHING;

-- name: GetWorkSourceEventByReceipt :one
SELECT e.* FROM work_source_events e JOIN work_source_receipts r ON r.event_id = e.id
WHERE r.namespace = ? AND r.receipt_key = ?;

-- name: GetWorkSourceEventByChange :one
SELECT * FROM work_source_events WHERE namespace = ? AND change_key = ?;

-- name: SetWorkSourceReceiptEvent :exec
UPDATE work_source_receipts SET event_id = ? WHERE namespace = ? AND receipt_key = ?;

-- name: StoreWorkSourceEvent :exec
INSERT INTO work_source_events (id, namespace, change_key, event_type, objects, revision, content,
  source_url, occurred_at, received_at, task_id, disposition)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: LinkWorkTaskObject :execrows
INSERT INTO work_task_objects (namespace, object_type, object_key, task_id)
SELECT @namespace, @object_type, @object_key, t.id FROM work_tasks t JOIN agents a ON a.id = t.agent_id
WHERE t.id = @task_id AND t.agent_id = @agent_id AND a.archived_at IS NULL
ON CONFLICT (namespace, object_type, object_key) DO UPDATE SET task_id = excluded.task_id
WHERE work_task_objects.task_id = excluded.task_id;

-- name: HasWorkTaskObject :one
SELECT count(*) FROM work_task_objects WHERE namespace = ? AND object_type = ? AND object_key = ?;

-- name: FindWorkTaskByObject :one
SELECT t.* FROM work_task_objects o JOIN work_tasks t ON t.id = o.task_id JOIN agents a ON a.id = t.agent_id
WHERE o.namespace = ? AND o.object_type = ? AND o.object_key = ? AND a.archived_at IS NULL;

-- name: AddWorkTaskWait :execrows
INSERT INTO work_task_waits (task_id, namespace, object_type, object_key, event_type, revision, after_at)
SELECT t.id, o.namespace, o.object_type, o.object_key, @event_type, @revision, @after_at
FROM work_task_objects o JOIN work_tasks t ON t.id = o.task_id JOIN agents a ON a.id = t.agent_id
WHERE t.id = @task_id AND t.agent_id = @agent_id AND t.lifecycle = 'open' AND a.archived_at IS NULL
AND o.namespace = @namespace AND o.object_type = @object_type AND o.object_key = @object_key
ON CONFLICT (task_id, namespace, object_type, object_key, event_type, revision, after_at) DO UPDATE SET after_at = excluded.after_at;

-- name: ResolveWorkTaskWaits :execrows
UPDATE work_task_waits SET resolved_by = @event_id
WHERE task_id = @task_id AND resolved_by IS NULL AND namespace = @namespace
AND object_type = @object_type AND object_key = @object_key AND event_type = @event_type
AND after_at < @occurred_at AND (revision = '' OR revision = sqlc.arg(event_revision));

-- name: ListWorkTaskWaits :many
SELECT w.* FROM work_task_waits w JOIN work_tasks t ON t.id = w.task_id JOIN agents a ON a.id = t.agent_id
WHERE w.task_id = @task_id AND t.agent_id = @agent_id AND a.archived_at IS NULL ORDER BY w.id;
