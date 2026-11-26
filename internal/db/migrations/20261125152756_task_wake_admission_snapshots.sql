-- +goose Up
CREATE TABLE task_wake_admission_snapshots (
  submission_id TEXT PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  task_id TEXT REFERENCES work_tasks(id)
) STRICT;

DROP INDEX submissions_one_unadmitted_task_wake;
CREATE UNIQUE INDEX submissions_one_unadmitted_task_wake ON submissions(conversation_id)
WHERE kind = 'task_wake' AND status = 'pending' AND admitted_at IS NULL;

-- +goose StatementBegin
CREATE TRIGGER release_task_wake_admission_snapshot AFTER UPDATE OF status ON submissions
WHEN NEW.kind = 'task_wake' AND NEW.status <> 'pending'
BEGIN
  DELETE FROM task_wake_admission_snapshots WHERE submission_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER release_task_wake_admission_snapshot;
DROP TABLE task_wake_admission_snapshots;
DROP INDEX submissions_one_unadmitted_task_wake;
-- The old index also counts terminal receipts that never reached native admission.
UPDATE submissions SET kind = 'task_result', delivered_at = coalesce(delivered_at, unixepoch() * 1000)
WHERE kind = 'task_wake' AND status <> 'pending' AND admitted_at IS NULL;
CREATE UNIQUE INDEX submissions_one_unadmitted_task_wake ON submissions(conversation_id)
WHERE kind = 'task_wake' AND admitted_at IS NULL;
