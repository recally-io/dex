-- +goose Up
-- Replace only the kind column. Rebuilding submissions would fire foreign key actions on its dependent rows.
DROP TRIGGER release_task_wake_admission_snapshot;
DROP INDEX submissions_one_unadmitted_task_wake;
DROP INDEX submissions_conversation_kind;
ALTER TABLE submissions RENAME COLUMN kind TO previous_kind;
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
  CHECK (kind IN ('message', 'session_result', 'wake', 'task_wake', 'context_reset'));
UPDATE submissions SET kind = CASE
  WHEN previous_kind <> 'task_result' THEN previous_kind
  WHEN id LIKE 'monitor:%' OR id LIKE 'background:%' OR origin = 'monitor' THEN 'wake'
  ELSE 'session_result'
END;
ALTER TABLE submissions DROP COLUMN previous_kind;

CREATE INDEX submissions_conversation_kind ON submissions (conversation_id, kind);
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
-- Replace only the kind column. Rebuilding submissions would fire foreign key actions on its dependent rows.
DROP TRIGGER release_task_wake_admission_snapshot;
DROP INDEX submissions_one_unadmitted_task_wake;
DROP INDEX submissions_conversation_kind;
ALTER TABLE submissions RENAME COLUMN kind TO previous_kind;
ALTER TABLE submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
  CHECK (kind IN ('message', 'task_result', 'task_wake', 'context_reset'));
UPDATE submissions SET kind = CASE
  WHEN previous_kind IN ('session_result', 'wake') THEN 'task_result'
  ELSE previous_kind
END;
ALTER TABLE submissions DROP COLUMN previous_kind;

CREATE INDEX submissions_conversation_kind ON submissions (conversation_id, kind);
CREATE UNIQUE INDEX submissions_one_unadmitted_task_wake ON submissions(conversation_id)
WHERE kind = 'task_wake' AND status = 'pending' AND admitted_at IS NULL;

-- +goose StatementBegin
CREATE TRIGGER release_task_wake_admission_snapshot AFTER UPDATE OF status ON submissions
WHEN NEW.kind = 'task_wake' AND NEW.status <> 'pending'
BEGIN
  DELETE FROM task_wake_admission_snapshots WHERE submission_id = NEW.id;
END;
-- +goose StatementEnd
