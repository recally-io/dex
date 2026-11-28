package task_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	tasksvc "github.com/recally-io/dex/internal/task"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestUserTaskBoardAccessMatrix(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()

	// Given a user-authorized Task with a reviewer, a linked Session and an external run.
	task := createBoardTestTask(t, svc, "Review the patch", "in_progress", "reviewer")
	boardExec(t, conn, `INSERT INTO task_sessions (id, task_id, bot_id, session_id, started_at)
		VALUES ('session-run', ?, 'linked', 4, 1)`, task.ID)
	boardExec(t, conn, `INSERT INTO task_external_runs (id, task_id, bot_id, harness, started_at)
		VALUES ('external-run', ?, 'linked', 'codex', 1)`, task.ID)
	task = storedTask(t, q, task.ID)
	unrelated := createBoardTestTask(t, svc, "Unrelated task", "ready", "")
	boardExec(t, conn, `UPDATE tasks SET creator_kind = 'source', authorized_by_root_id = NULL WHERE id = ?`, unrelated.ID)
	unrelated = storedTask(t, q, unrelated.ID)
	actorAt := func(id int64) tasksvc.Actor {
		t.Helper()
		conv, err := q.GetSession(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return tasksvc.BotInConversation(conv)
	}

	// Each permission string lists all allowed actions for that actor and origin.
	actions := []struct {
		name string
		code rune
		item tasksvc.Action
	}{
		{"post", 'p', tasksvc.ActionPost},
		{"transition", 't', tasksvc.ActionTransition},
		{"complete", 'f', tasksvc.ActionComplete},
		{"link-session", 'k', tasksvc.ActionLinkSession},
		{"rename", 'n', tasksvc.ActionRename},
		{"submit-review", 's', tasksvc.ActionSubmitReview},
		{"review", 'r', tasksvc.ActionReview},
		{"split", 'd', tasksvc.ActionSplit},
		{"create-top-level", 'c', tasksvc.ActionCreateTopLevel},
		{"owner-lifecycle", 'l', tasksvc.ActionOwnerLifecycle},
	}
	origins := []struct {
		name string
		item tasksvc.Origin
	}{
		{"user", tasksvc.OriginUser},
		{"task-wake", tasksvc.OriginTaskWake},
		{"session-result", tasksvc.OriginSessionResult},
		{"wake", tasksvc.OriginWake},
		{"other", tasksvc.OriginOther},
		{"native-result", tasksvc.OriginNativeResult},
	}
	for _, row := range []struct {
		name                      string
		actor                     tasksvc.Actor
		read                      bool
		user, wake, result, other string
		native                    string
	}{
		{"owner", tasksvc.Owner, true, "ptfknsrdcl", "tfknsd", "", "", ""},
		{"assignee-main", actorAt(1), true, "ptfknsdc", "ptfknsd", "p", "", "pfs"},
		{"reviewer-main", tasksvc.Bot("reviewer"), true, "prc", "pr", "p", "", "p"},
		{"unrelated-main", actorAt(2), false, "c", "", "", "", ""},
		{"linked-main", actorAt(7), false, "c", "", "", "", ""},
		{"assignee-session", actorAt(6), true, "pknc", "p", "p", "", "p"},
		{"linked-bot-session", actorAt(8), false, "c", "", "", "", ""},
		{"unlinked-assignee-child", actorAt(3), false, "", "", "", "", ""},
		{"linked-session", actorAt(4), true, "p", "p", "p", "", "p"},
		{"unlinked-session-same-bot", actorAt(5), false, "", "", "", "", ""},
		{"unrelated-bot-linked-session-id", tasksvc.SessionBot("other", 4), false, "", "", "", "", ""},
		{"external-run", tasksvc.ExternalAgent("external-run"), true, "p", "p", "p", "", "p"},
		{"session-run-as-external-run", tasksvc.ExternalAgent("session-run"), false, "", "", "", "", ""},
		{"unbound-external-run", tasksvc.ExternalAgent("unknown-run"), false, "", "", "", "", ""},
		{"source", tasksvc.Source, false, "", "", "", "", ""},
		{"system", tasksvc.System, false, "", "", "", "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if read, err := tasksvc.CanRead(ctx, q, row.actor, task); err != nil || read != row.read {
				t.Errorf("CanRead = %v, %v; want %v, nil", read, err, row.read)
			}
			permissions := []string{row.user, row.wake, row.result, "", row.other, row.native}
			for i, origin := range origins {
				for _, action := range actions {
					target := task
					if action.item == tasksvc.ActionCreateTopLevel {
						target = sqlc.Task{}
					}
					want := strings.ContainsRune(permissions[i], action.code)
					if allow, err := tasksvc.CanWrite(ctx, q, row.actor, origin.item, target, action.item); err != nil || allow != want {
						t.Errorf("CanWrite(%s, %s) = %v, %v; want %v, nil", origin.name, action.name, allow, err, want)
					}
				}
			}
		})
	}

	for name, actor := range map[string]tasksvc.Actor{
		"linked-session": tasksvc.SessionBot("linked", 4),
		"external-run":   tasksvc.ExternalAgent("external-run"),
	} {
		if read, err := tasksvc.CanRead(ctx, q, actor, unrelated); err != nil || read {
			t.Errorf("%s read other Task = %v, %v; want false, nil", name, read, err)
		}
		if allow, err := tasksvc.CanWrite(ctx, q, actor, tasksvc.OriginUser, unrelated, tasksvc.ActionPost); err != nil || allow {
			t.Errorf("%s post to other Task = %v, %v; want false, nil", name, allow, err)
		}
	}
	for name, actor := range map[string]tasksvc.Actor{"owner": tasksvc.Owner, "assignee": tasksvc.Bot("assignee")} {
		if allow, err := tasksvc.CanWrite(ctx, q, actor, tasksvc.OriginUser, unrelated, tasksvc.ActionSplit); err != nil || allow {
			t.Errorf("%s split without an authorized root = %v, %v; want false, nil", name, allow, err)
		}
	}
}

func TestUserTaskBoardLinksOnlyDirectChildrenAcrossBots(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()
	task := createBoardTestTask(t, svc, "Session links", "ready", "")
	assignee := tasksvc.Bot("assignee")

	for _, step := range []struct {
		name    string
		actor   tasksvc.Actor
		child   int64
		promote bool
		want    error
	}{
		{"link direct child from another Bot", assignee, 4, false, nil},
		{"link standalone Session", assignee, 6, false, tasksvc.ErrForbidden},
		{"link child of another parent", assignee, 5, false, tasksvc.ErrForbidden},
		{"Owner links child of another parent", tasksvc.Owner, 5, false, nil},
		{"promote child of another parent", assignee, 5, true, tasksvc.ErrForbidden},
		{"link direct child of the same Bot", assignee, 3, false, nil},
		{"Owner promotes direct child", tasksvc.Owner, 3, true, nil},
		{"promote direct child from another Bot", assignee, 4, true, nil},
	} {
		err := commitLedger(t, conn, svc, func(q *sqlc.Queries, ledger *tasksvc.Ledger) error {
			current, err := q.GetTask(ctx, task.ID)
			if err != nil {
				return err
			}
			if err := tasksvc.AuthorizeSessionLink(ctx, q, step.actor, tasksvc.OriginUser, current, step.child); err != nil {
				return err
			}
			return ledger.LinkSession(ctx, step.actor, current, step.child, step.promote, time.Now().UnixMilli())
		})
		if !errors.Is(err, step.want) {
			t.Fatalf("%s: got %v, want %v", step.name, err, step.want)
		}
	}
	links, err := q.ListTaskSessions(ctx, task.ID)
	if err != nil || len(links) != 3 || links[0].SessionID != 4 || links[0].IsPrimary != 1 {
		t.Fatalf("links = %+v, %v; want direct child 4 primary of three links", links, err)
	}
}

func TestUserTaskBoardLedgerCommitsBeforeNotification(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()

	// Reserve a separate SQLite connection so the hook can only see committed rows.
	reader, err := conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	var published []int64
	svc.SetOnEntry(func(id int64) {
		published = append(published, id)
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var seq, nextSeq int64
		err := reader.QueryRowContext(readCtx, `SELECT e.seq, t.next_entry_seq
			FROM task_entries e JOIN tasks t ON t.id = e.task_id WHERE e.id = ?`, id).Scan(&seq, &nextSeq)
		if err != nil || nextSeq != seq+1 {
			t.Errorf("hook for entry %d sees seq=%d, next seq=%d, err=%v; want committed entry and projection", id, seq, nextSeq, err)
		}
	})

	first := createBoardTestTask(t, svc, "First Task", "ready", "")
	second := createBoardTestTask(t, svc, "Second Task", "ready", "")
	firstEntry, secondEntry := taskEntry(t, q, first.ID, 1), taskEntry(t, q, second.ID, 1)
	if first.Number != 1 || second.Number != 2 || firstEntry.ID >= secondEntry.ID {
		t.Fatalf("Task numbers = %d, %d; initial entry IDs = %d, %d; want increasing global numbers and IDs", first.Number, second.Number, firstEntry.ID, secondEntry.ID)
	}
	if first.NextEntrySeq != 2 || second.NextEntrySeq != 2 || firstEntry.Kind != "note" ||
		firstEntry.Content != "Task brief" || firstEntry.MetaJson != "{}" || firstEntry.ActorKind != "owner" || firstEntry.Trust != "owner" {
		t.Fatalf("initial ledger: Tasks %+v, %+v; entries %+v, %+v", first, second, firstEntry, secondEntry)
	}

	note, err := svc.PostEntry(ctx, tasksvc.Bot("assignee"), tasksvc.OriginTaskWake, first.ID, tasksvc.PostEntryParams{
		Kind: "note", Tag: "decision", Content: "Use the existing interface",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if note.Seq != 2 || note.ID <= secondEntry.ID || note.Tag != "decision" || note.Content != "Use the existing interface" ||
		note.ActorKind != "bot" || note.ActorBotID != "assignee" || note.Trust != "agent" || taskEntry(t, q, first.ID, 2) != note {
		t.Fatalf("posted note: %+v", note)
	}
	afterNote := storedTask(t, q, first.ID)
	if afterNote.Revision != first.Revision+1 || afterNote.NextEntrySeq != 3 || afterNote.Status != first.Status || afterNote.UpdatedAt < first.UpdatedAt {
		t.Fatalf("note projection: before %+v; after %+v", first, afterNote)
	}

	if _, err := svc.UpdateTask(ctx, first.ID, tasksvc.UpdateParams{Status: new(tasksvc.StatusInProgress), Revision: &first.Revision}); !errors.Is(err, tasksvc.ErrRevision) {
		t.Fatalf("stale If-Match: got %v; want ErrRevision", err)
	}
	for _, kind := range []string{"status", "assign", "review", "execution", "external", "tombstone"} {
		if _, err := svc.PostEntry(ctx, tasksvc.Owner, tasksvc.OriginUser, first.ID, tasksvc.PostEntryParams{Kind: kind, Content: "Forged event"}, nil); err == nil {
			t.Errorf("generic post accepted privileged kind %q", kind)
		}
	}
	assertBoardUnchanged(t, q, afterNote)

	// A deferred foreign key permits the ledger INSERT and fails the actual commit.
	boardExec(t, conn, `CREATE TABLE board_test_commit_failure (
		task_id TEXT NOT NULL REFERENCES tasks(id) DEFERRABLE INITIALLY DEFERRED
	) STRICT`)
	boardExec(t, conn, `CREATE TRIGGER board_test_fail_commit AFTER INSERT ON task_entries
		WHEN NEW.kind = 'status' BEGIN INSERT INTO board_test_commit_failure VALUES ('missing-task'); END`)
	update := tasksvc.UpdateParams{Status: new(tasksvc.StatusInProgress), Revision: &afterNote.Revision}
	_, err = svc.UpdateTask(ctx, first.ID, update)
	if sqliteErr, ok := errors.AsType[*sqlite.Error](err); !ok || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		t.Fatalf("status change error = %v; want deferred foreign key violation", err)
	}
	if want := []int64{firstEntry.ID, secondEntry.ID, note.ID}; !slices.Equal(published, want) {
		t.Errorf("notifications after rejected writes = %v; want %v", published, want)
	}
	assertBoardUnchanged(t, q, afterNote)
	boardExec(t, conn, `DROP TRIGGER board_test_fail_commit`)

	updated, err := svc.UpdateTask(ctx, first.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	progress := updated.Task
	status := taskEntry(t, q, first.ID, 3)
	if status.Kind != "status" || progress.Status != "in_progress" || progress.Revision != afterNote.Revision+1 || progress.NextEntrySeq != 4 {
		t.Fatalf("status entry %+v; projection %+v", status, progress)
	}

	visible, err := q.ListTaskEntries(ctx, first.ID)
	if err != nil || !slices.Contains(visible, note) {
		t.Fatalf("visible ledger before retraction: %+v, %v", visible, err)
	}
	// A database from an older release already contains this retraction.
	boardExec(t, conn, `INSERT INTO task_entries (task_id, seq, kind, meta_json, content, created_at)
		VALUES (?, ?, 'tombstone', json_object('targetSeq', ?), '', 1)`, first.ID, progress.NextEntrySeq, note.Seq)
	visible, err = q.ListTaskEntries(ctx, first.ID)
	if err != nil || !slices.Equal(visible, []sqlc.TaskEntry{firstEntry, status}) {
		t.Fatalf("visible ledger after retraction: %+v, %v", visible, err)
	}
	if want := []int64{firstEntry.ID, secondEntry.ID, note.ID, status.ID}; !slices.Equal(published, want) {
		t.Errorf("notifications = %v; want %v", published, want)
	}
}

func TestUserTaskBoardReservesCancelAndReopenForOwner(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()
	assignee := tasksvc.Bot("assignee")
	task := createBoardTestTask(t, svc, "Lifecycle control", "backlog", "")
	ready, err := transitionTaskWithLedger(t, conn, svc, task.ID, assignee, tasksvc.OriginTaskWake, "ready")
	if err != nil || ready.Status != "ready" {
		t.Fatalf("move backlog Task to ready: %+v, %v", ready, err)
	}

	for _, status := range []string{"cancelled", "blocked"} {
		if _, err := transitionTaskWithLedger(t, conn, svc, task.ID, assignee, tasksvc.OriginUser, status); err == nil {
			t.Errorf("assignee set status to %q", status)
		}
	}
	assertBoardUnchanged(t, q, ready)
	cancelled, err := svc.UpdateTask(ctx, task.ID, tasksvc.UpdateParams{Status: new(tasksvc.StatusCancelled)})
	if err != nil || cancelled.Task.Status != "cancelled" || storedTask(t, q, task.ID) != cancelled.Task {
		t.Fatalf("Owner cancelled Task: %+v, %v", cancelled, err)
	}

	if _, err := transitionTaskWithLedger(t, conn, svc, task.ID, assignee, tasksvc.OriginUser, "in_progress"); err == nil {
		t.Fatal("assignee reopened a cancelled Task")
	}
	assertBoardUnchanged(t, q, cancelled.Task)
	reopened, err := svc.UpdateTask(ctx, task.ID, tasksvc.UpdateParams{Status: new(tasksvc.StatusInProgress)})
	if err != nil || reopened.Task.Status != "in_progress" || storedTask(t, q, task.ID) != reopened.Task {
		t.Fatalf("Owner reopened Task: %+v, %v", reopened, err)
	}
}

func TestUserTaskReviewBindsApprovalToLatestSubmission(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()
	assignee, reviewer := tasksvc.Bot("assignee"), tasksvc.Bot("reviewer")
	task := createBoardTestTask(t, svc, "Review work", "in_progress", "reviewer")

	submit := func(before sqlc.Task) sqlc.Task {
		t.Helper()
		after, err := transitionTaskWithLedger(t, conn, svc, before.ID, assignee, tasksvc.OriginTaskWake, "in_review")
		if err != nil {
			t.Fatal(err)
		}
		entry := taskEntry(t, q, before.ID, before.NextEntrySeq)
		if after.Status != "in_review" || after.ReviewSeq != entry.Seq || entry.Kind != "status" ||
			after.Revision != before.Revision+1 || storedTask(t, q, before.ID) != after {
			t.Fatalf("review entry %+v; projection %+v", entry, after)
		}
		return after
	}
	first := submit(task)
	if _, err := svc.Review(ctx, assignee, tasksvc.OriginUser, task.ID, "approve", first.ReviewSeq, ""); err == nil {
		t.Fatal("the assignee approved their own work without being the reviewer")
	}
	assertBoardUnchanged(t, q, first)

	const feedback = "Include the failure case"
	changes, err := svc.Review(ctx, reviewer, tasksvc.OriginTaskWake, task.ID, "request_changes", first.ReviewSeq, feedback)
	if err != nil {
		t.Fatal(err)
	}
	entry := taskEntry(t, q, task.ID, first.NextEntrySeq)
	if changes.Task.Status != "in_progress" || changes.Task.Revision != first.Revision+1 || changes.Task.NextEntrySeq != entry.Seq+1 ||
		entry.Kind != "review" || entry.Content != feedback || entry.ActorBotID != "reviewer" || storedTask(t, q, task.ID) != changes.Task {
		t.Fatalf("change request entry %+v; projection %+v", entry, changes)
	}
	latest := submit(changes.Task)
	if latest.ReviewSeq <= first.ReviewSeq {
		t.Fatalf("resubmission review seq = %d; want greater than %d", latest.ReviewSeq, first.ReviewSeq)
	}
	artifact, err := svc.PostEntry(ctx, assignee, tasksvc.OriginTaskWake, task.ID, tasksvc.PostEntryParams{
		Kind: "artifact", Content: "Test report",
	}, nil)
	if err != nil || artifact.Kind != "artifact" || artifact.Seq != latest.NextEntrySeq {
		t.Fatalf("posted artifact: %+v, %v", artifact, err)
	}
	withArtifact := storedTask(t, q, task.ID)
	if withArtifact.ReviewSeq != latest.ReviewSeq || withArtifact.Status != "in_review" || withArtifact.Revision != latest.Revision+1 {
		t.Fatalf("artifact changed review binding: before %+v; after %+v", latest, withArtifact)
	}
	if _, err := svc.Review(ctx, reviewer, tasksvc.OriginTaskWake, task.ID, "approve", first.ReviewSeq, ""); !errors.Is(err, tasksvc.ErrConflict) {
		t.Fatalf("approval of old submission: got %v; want ErrConflict", err)
	}
	assertBoardUnchanged(t, q, withArtifact)

	done, err := svc.Review(ctx, reviewer, tasksvc.OriginTaskWake, task.ID, "approve", latest.ReviewSeq, "")
	if err != nil {
		t.Fatal(err)
	}
	approval := taskEntry(t, q, task.ID, withArtifact.NextEntrySeq)
	if done.Task.Status != "done" || done.Task.Revision != withArtifact.Revision+1 || done.Task.NextEntrySeq != approval.Seq+1 ||
		approval.Kind != "review" || approval.ActorBotID != "reviewer" || storedTask(t, q, task.ID) != done.Task {
		t.Fatalf("approval entry %+v; projection %+v", approval, done)
	}
}

func TestUserTaskBoardDerivesBlockersAndMainQueuePosition(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()
	task := createBoardTestTask(t, svc, "Blocked Task", "in_progress", "")
	other := createBoardTestTask(t, svc, "Other Task", "ready", "")
	boardExec(t, conn, `INSERT INTO task_source_events (id, namespace, event_type, objects, revision, content, source_url,
		occurred_at, received_at, task_id, disposition) VALUES ('merged', 'github', 'merged', '[]', 'r1', '', '', 2, 2, ?, 'attached')`, task.ID)
	boardExec(t, conn, `INSERT INTO task_waits (id, task_id, namespace, object_type, object_key, event_type, revision, after_at, resolved_by)
		VALUES (10, ?, 'github', 'pull', '10', 'merged', 'r1', 1, NULL),
		       (11, ?, 'github', 'pull', '11', 'merged', 'r1', 1, 'merged'),
		       (12, ?, 'github', 'pull', '12', 'merged', 'r1', 1, NULL)`, task.ID, task.ID, other.ID)

	seedQuestion := func(id, taskID, botID, status, submissionStatus string, sessionID, queued int64) {
		t.Helper()
		boardExec(t, conn, `INSERT INTO submissions (id, session_id, kind, content, status, created_at)
			VALUES (?, ?, 'session_result', '', ?, 1)`, id, sessionID, submissionStatus)
		if taskID != "" {
			boardExec(t, conn, `INSERT INTO task_inputs (submission_id, task_id) VALUES (?, ?)`, id, taskID)
		}
		boardExec(t, conn, `INSERT INTO questions (id, bot_id, session_id, source_submission_id, tool_operation_key,
			text, options_json, status, queued, created_at) VALUES (?, ?, ?, ?, ?, 'Proceed?', '[]', ?, ?, 1)`,
			id, botID, sessionID, id, id, status, queued)
	}
	seedQuestion("answered", task.ID, "assignee", "answered", "done", 1, 1)
	seedQuestion("not-a-task", "", "assignee", "open", "done", 1, 0)
	seedQuestion("unsettled", task.ID, "assignee", "open", "pending", 1, 1)
	seedQuestion("another-main", other.ID, "other", "open", "done", 2, 0)
	seedQuestion("first-in-queue", other.ID, "assignee", "open", "done", 1, 1)
	seedQuestion("second-in-queue", task.ID, "assignee", "open", "done", 1, 1)
	seedQuestion("child-question", task.ID, "assignee", "open", "done", 3, 0)

	assertBlockers := func(waitID int64, questions map[string]int64) {
		t.Helper()
		blockers, err := tasksvc.ComputeBlockers(ctx, q, task)
		if err != nil {
			t.Fatal(err)
		}
		count := len(questions)
		if waitID != 0 {
			count++
		}
		if len(blockers) != count {
			t.Fatalf("blockers = %+v; want wait %d and questions %v", blockers, waitID, questions)
		}
		for _, blocker := range blockers {
			switch blocker.Kind {
			case "wait":
				if waitID == 0 || blocker.WaitID != waitID {
					t.Errorf("unexpected wait blocker %+v; want wait %d", blocker, waitID)
				}
				waitID = 0
			case "question":
				if position, ok := questions[blocker.QuestionID]; !ok || position != blocker.QueuePosition {
					t.Errorf("unexpected question blocker %+v; remaining questions %v", blocker, questions)
				}
				delete(questions, blocker.QuestionID)
			default:
				t.Errorf("unexpected blocker kind: %+v", blocker)
			}
		}
	}
	assertBlockers(10, map[string]int64{"second-in-queue": 3, "child-question": 0})

	closed, err := svc.UpdateTask(ctx, task.ID, tasksvc.UpdateParams{Status: new(tasksvc.StatusDone)})
	if err != nil || closed.BlockerCount != 0 {
		t.Fatalf("closed Task blockers = %d, err = %v; want 0, nil", closed.BlockerCount, err)
	}
	reopened, err := svc.UpdateTask(ctx, task.ID, tasksvc.UpdateParams{Status: new(tasksvc.StatusInProgress)})
	if err != nil {
		t.Fatal(err)
	}
	task = reopened.Task
	boardExec(t, conn, `UPDATE task_waits SET resolved_by = 'merged' WHERE id = 10`)
	boardExec(t, conn, `UPDATE questions SET status = 'answered' WHERE id = 'first-in-queue'`)
	assertBlockers(0, map[string]int64{"second-in-queue": 2, "child-question": 0})
	boardExec(t, conn, `UPDATE questions SET status = 'answered' WHERE id IN ('second-in-queue', 'child-question')`)
	assertBlockers(0, nil)
	assertBoardUnchanged(t, q, task)
}

func openBoardTest(t *testing.T) (*sql.DB, *sqlc.Queries, *tasksvc.Service) {
	t.Helper()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	boardExec(t, conn, `INSERT INTO bots (id, name, model_provider, model_id, created_at) VALUES
		('assignee', 'Assignee', 'faux', 'echo', 1), ('reviewer', 'Reviewer', 'faux', 'echo', 1),
		('linked', 'Linked', 'faux', 'echo', 1), ('other', 'Other', 'faux', 'echo', 1)`)
	boardExec(t, conn, `INSERT INTO sessions (id, bot_id, channel_key, created_at, parent_id) VALUES
		(1, 'assignee', 'main', 1, NULL), (2, 'other', 'main', 1, NULL), (3, 'assignee', 'chat:child', 1, 1),
		(4, 'linked', 'chat:linked', 1, 1), (5, 'linked', 'chat:other-child', 1, 2),
		(6, 'assignee', 'chat:standalone', 1, NULL), (7, 'linked', 'main', 1, NULL),
		(8, 'linked', 'chat:standalone', 1, NULL)`)
	return conn, sqlc.New(conn), tasksvc.New(conn, nil)
}

func transitionTaskWithLedger(t *testing.T, conn *sql.DB, svc *tasksvc.Service, id string, actor tasksvc.Actor, origin tasksvc.Origin, status string) (sqlc.Task, error) {
	t.Helper()
	var updated sqlc.Task
	err := commitLedger(t, conn, svc, func(q *sqlc.Queries, ledger *tasksvc.Ledger) error {
		var err error
		updated, err = ledger.TransitionStatus(t.Context(), actor, origin, storedTask(t, q, id), status, time.Now().UnixMilli())
		return err
	})
	return updated, err
}

func commitLedger(t *testing.T, conn *sql.DB, svc *tasksvc.Service, change func(*sqlc.Queries, *tasksvc.Ledger) error) error {
	t.Helper()
	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	q := sqlc.New(tx)
	ledger := tasksvc.NewLedger(q)
	if err := change(q, ledger); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	svc.Notify(ledger)
	return nil
}

func boardExec(t *testing.T, conn *sql.DB, stmt string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), stmt, args...); err != nil {
		t.Fatalf("execute %s: %v", stmt, err)
	}
}

func createBoardTestTask(t *testing.T, svc *tasksvc.Service, title, status, reviewerID string) sqlc.Task {
	t.Helper()
	params := tasksvc.CreateTaskParams{AssigneeBotID: "assignee", Title: title, Status: status, Note: "Task brief"}
	if reviewerID != "" {
		params.ReviewerKind, params.ReviewerID = "bot", reviewerID
	}
	task, err := svc.CreateTask(t.Context(), tasksvc.Owner, tasksvc.OriginUser, params)
	if err != nil {
		t.Fatalf("create Task %q: %v", title, err)
	}
	return task
}

func storedTask(t *testing.T, q *sqlc.Queries, id string) sqlc.Task {
	t.Helper()
	task, err := q.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("read Task %s: %v", id, err)
	}
	return task
}

func taskEntry(t *testing.T, q *sqlc.Queries, id string, seq int64) sqlc.TaskEntry {
	t.Helper()
	entry, err := q.GetTaskEntry(t.Context(), sqlc.GetTaskEntryParams{TaskID: id, Seq: seq})
	if err != nil {
		t.Fatalf("read Task %s entry %d: %v", id, seq, err)
	}
	return entry
}

func assertBoardUnchanged(t *testing.T, q *sqlc.Queries, want sqlc.Task) {
	t.Helper()
	if got := storedTask(t, q, want.ID); got != want {
		t.Errorf("Task projection changed: got %+v; want %+v", got, want)
	}
	if entry, err := q.GetTaskEntry(t.Context(), sqlc.GetTaskEntryParams{TaskID: want.ID, Seq: want.NextEntrySeq}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("next ledger sequence %d should be unused: got %+v, %v", want.NextEntrySeq, entry, err)
	}
}
