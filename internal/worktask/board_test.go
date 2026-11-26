package worktask_test

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
	"github.com/recally-io/dex/internal/worktask"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestUserTaskBoardAccessMatrix(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()

	// Given a user-authorized Task with a reviewer, a linked Session and an external run.
	task := createBoardTestTask(t, svc, "Review the patch", "in_progress", "reviewer")
	boardExec(t, conn, `INSERT INTO task_executions (id, task_id, bot_id, kind, conversation_id, started_at)
		VALUES ('session-run', ?, 'linked', 'dex_session', 4, 1), ('external-run', ?, 'linked', 'external_agent', NULL, 1)`, task.ID, task.ID)
	task = boardTask(t, q, task.ID)
	unrelated := createBoardTestTask(t, svc, "Unrelated task", "ready", "")
	boardExec(t, conn, `UPDATE work_tasks SET creator_kind = 'source', authorized_by_root_id = NULL WHERE id = ?`, unrelated.ID)
	unrelated = boardTask(t, q, unrelated.ID)
	actorAt := func(id int64) worktask.Actor {
		t.Helper()
		conv, err := q.GetConversation(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return worktask.BotInConversation(conv)
	}

	// Each permission string lists all allowed actions for that actor and origin.
	actions := []struct {
		name string
		code rune
		item worktask.Action
	}{
		{"post", 'p', worktask.ActionPost},
		{"transition", 't', worktask.ActionTransition},
		{"link-session", 'k', worktask.ActionLinkSession},
		{"rename", 'n', worktask.ActionRename},
		{"submit-review", 's', worktask.ActionSubmitReview},
		{"review", 'r', worktask.ActionReview},
		{"split", 'd', worktask.ActionSplit},
		{"create-top-level", 'c', worktask.ActionCreateTopLevel},
		{"owner-lifecycle", 'l', worktask.ActionOwnerLifecycle},
		{"tombstone", 'x', worktask.ActionTombstone},
	}
	origins := []struct {
		name string
		item worktask.Origin
	}{
		{"user", worktask.OriginUser},
		{"task-wake", worktask.OriginTaskWake},
		{"task-result", worktask.OriginTaskResult},
		{"other", worktask.OriginOther},
	}
	for _, row := range []struct {
		name                      string
		actor                     worktask.Actor
		read                      bool
		user, wake, result, other string
	}{
		{"owner", worktask.Owner, true, "ptknsrdclx", "tknsd", "", ""},
		{"assignee-main", actorAt(1), true, "ptknsdc", "ptknsd", "p", ""},
		{"reviewer-main", worktask.Bot("reviewer"), true, "prc", "pr", "p", ""},
		{"unrelated-main", actorAt(2), false, "c", "", "", ""},
		{"linked-main", actorAt(7), false, "c", "", "", ""},
		{"assignee-chat", actorAt(6), true, "pknc", "p", "p", ""},
		{"linked-bot-chat", actorAt(8), false, "c", "", "", ""},
		{"unlinked-assignee-child", actorAt(3), false, "", "", "", ""},
		{"linked-session", actorAt(4), true, "p", "p", "p", ""},
		{"unlinked-session-same-bot", actorAt(5), false, "", "", "", ""},
		{"unrelated-bot-linked-session-id", worktask.SessionBot("other", 4), false, "", "", "", ""},
		{"external-run", worktask.ExternalAgent("external-run"), true, "p", "p", "p", ""},
		{"session-run-as-external-run", worktask.ExternalAgent("session-run"), false, "", "", "", ""},
		{"unbound-external-run", worktask.ExternalAgent("unknown-run"), false, "", "", "", ""},
		{"source", worktask.Source, false, "", "", "", ""},
		{"system", worktask.System, false, "", "", "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if read, err := worktask.CanRead(ctx, q, row.actor, task); err != nil || read != row.read {
				t.Errorf("CanRead = %v, %v; want %v, nil", read, err, row.read)
			}
			permissions := []string{row.user, row.wake, row.result, row.other}
			for i, origin := range origins {
				for _, action := range actions {
					target := task
					if action.item == worktask.ActionCreateTopLevel {
						target = sqlc.WorkTask{}
					}
					want := strings.ContainsRune(permissions[i], action.code)
					if allow, err := worktask.CanWrite(ctx, q, row.actor, origin.item, target, action.item); err != nil || allow != want {
						t.Errorf("CanWrite(%s, %s) = %v, %v; want %v, nil", origin.name, action.name, allow, err, want)
					}
				}
			}
		})
	}

	for name, actor := range map[string]worktask.Actor{
		"linked-session": worktask.SessionBot("linked", 4),
		"external-run":   worktask.ExternalAgent("external-run"),
	} {
		if read, err := worktask.CanRead(ctx, q, actor, unrelated); err != nil || read {
			t.Errorf("%s read other Task = %v, %v; want false, nil", name, read, err)
		}
		if allow, err := worktask.CanWrite(ctx, q, actor, worktask.OriginUser, unrelated, worktask.ActionPost); err != nil || allow {
			t.Errorf("%s post to other Task = %v, %v; want false, nil", name, allow, err)
		}
	}
	for name, actor := range map[string]worktask.Actor{"owner": worktask.Owner, "assignee": worktask.Bot("assignee")} {
		if allow, err := worktask.CanWrite(ctx, q, actor, worktask.OriginUser, unrelated, worktask.ActionSplit); err != nil || allow {
			t.Errorf("%s split without an authorized root = %v, %v; want false, nil", name, allow, err)
		}
	}
}

func TestUserTaskBoardLinksOnlyDirectChildrenAcrossBots(t *testing.T) {
	_, q, svc := openBoardTest(t)
	ctx := t.Context()
	task := createBoardTestTask(t, svc, "Session links", "ready", "")
	assignee := worktask.Bot("assignee")

	if err := svc.LinkSession(ctx, assignee, worktask.OriginUser, task.ID, 4); err != nil {
		t.Fatalf("link direct child from another Bot: %v", err)
	}
	if err := svc.LinkSession(ctx, assignee, worktask.OriginUser, task.ID, 6); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("link standalone Chat of the assignee: %v; want ErrForbidden", err)
	}
	if err := svc.LinkSession(ctx, assignee, worktask.OriginUser, task.ID, 5); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("link child of another parent: %v; want ErrForbidden", err)
	}
	if err := svc.LinkSession(ctx, worktask.Owner, worktask.OriginUser, task.ID, 5); err != nil {
		t.Fatalf("Owner link: %v", err)
	}
	if err := svc.PromotePrimarySession(ctx, assignee, worktask.OriginUser, task.ID, 5); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("promote child of another parent: %v; want ErrForbidden", err)
	}
	if err := svc.LinkSession(ctx, assignee, worktask.OriginUser, task.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err := svc.PromotePrimarySession(ctx, worktask.Owner, worktask.OriginUser, task.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err := svc.PromotePrimarySession(ctx, assignee, worktask.OriginUser, task.ID, 4); err != nil {
		t.Fatalf("promote direct child from another Bot: %v", err)
	}
	links, err := q.ListWorkTaskSessions(ctx, task.ID)
	if err != nil || len(links) != 3 || links[0].ConversationID != 4 || links[0].IsPrimary != 1 {
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
			FROM work_task_entries e JOIN work_tasks t ON t.id = e.task_id WHERE e.id = ?`, id).Scan(&seq, &nextSeq)
		if err != nil || nextSeq != seq+1 {
			t.Errorf("hook for entry %d sees seq=%d, next seq=%d, err=%v; want committed entry and projection", id, seq, nextSeq, err)
		}
	})

	first := createBoardTestTask(t, svc, "First Task", "ready", "")
	second := createBoardTestTask(t, svc, "Second Task", "ready", "")
	firstEntry, secondEntry := boardEntry(t, q, first.ID, 1), boardEntry(t, q, second.ID, 1)
	if first.Number != 1 || second.Number != 2 || firstEntry.ID >= secondEntry.ID {
		t.Fatalf("Task numbers = %d, %d; initial entry IDs = %d, %d; want increasing global numbers and IDs", first.Number, second.Number, firstEntry.ID, secondEntry.ID)
	}
	if first.NextEntrySeq != 2 || second.NextEntrySeq != 2 || firstEntry.Kind != "note" ||
		firstEntry.Content != "Task brief" || firstEntry.MetaJson != "{}" || firstEntry.ActorKind != "owner" || firstEntry.Trust != "owner" {
		t.Fatalf("initial ledger: Tasks %+v, %+v; entries %+v, %+v", first, second, firstEntry, secondEntry)
	}

	note, err := svc.PostEntry(ctx, worktask.Bot("assignee"), worktask.OriginTaskWake, first.ID, worktask.PostEntryParams{
		Kind: "note", Tag: "decision", Content: "Use the existing interface",
	})
	if err != nil {
		t.Fatal(err)
	}
	if note.Seq != 2 || note.ID <= secondEntry.ID || note.Tag != "decision" || note.Content != "Use the existing interface" ||
		note.ActorKind != "bot" || note.ActorBotID != "assignee" || note.Trust != "agent" || boardEntry(t, q, first.ID, 2) != note {
		t.Fatalf("posted note: %+v", note)
	}
	afterNote := boardTask(t, q, first.ID)
	if afterNote.Revision != first.Revision+1 || afterNote.NextEntrySeq != 3 || afterNote.Status != first.Status || afterNote.UpdatedAt < first.UpdatedAt {
		t.Fatalf("note projection: before %+v; after %+v", first, afterNote)
	}

	if _, err := svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, first.ID, "in_progress", &first.Revision); !errors.Is(err, worktask.ErrConflict) {
		t.Fatalf("stale If-Match: got %v; want ErrConflict", err)
	}
	for _, kind := range []string{"status", "assign", "review", "execution", "external", "tombstone"} {
		if _, err := svc.PostEntry(ctx, worktask.Owner, worktask.OriginUser, first.ID, worktask.PostEntryParams{Kind: kind, Content: "Forged event"}); err == nil {
			t.Errorf("generic post accepted privileged kind %q", kind)
		}
	}
	assertBoardUnchanged(t, q, afterNote)

	// A deferred foreign key permits the ledger INSERT and fails the actual commit.
	boardExec(t, conn, `CREATE TABLE board_test_commit_failure (
		task_id TEXT NOT NULL REFERENCES work_tasks(id) DEFERRABLE INITIALLY DEFERRED
	) STRICT`)
	boardExec(t, conn, `CREATE TRIGGER board_test_fail_commit AFTER INSERT ON work_task_entries
		WHEN NEW.kind = 'status' BEGIN INSERT INTO board_test_commit_failure VALUES ('missing-task'); END`)
	_, err = svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, first.ID, "in_progress", &afterNote.Revision)
	if sqliteErr, ok := errors.AsType[*sqlite.Error](err); !ok || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		t.Fatalf("status change error = %v; want deferred foreign key violation", err)
	}
	if want := []int64{firstEntry.ID, secondEntry.ID, note.ID}; !slices.Equal(published, want) {
		t.Errorf("notifications after rejected writes = %v; want %v", published, want)
	}
	assertBoardUnchanged(t, q, afterNote)
	boardExec(t, conn, `DROP TRIGGER board_test_fail_commit`)

	progress, err := svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, first.ID, "in_progress", &afterNote.Revision)
	if err != nil {
		t.Fatal(err)
	}
	status := boardEntry(t, q, first.ID, 3)
	if status.Kind != "status" || progress.Status != "in_progress" || progress.Revision != afterNote.Revision+1 || progress.NextEntrySeq != 4 {
		t.Fatalf("status entry %+v; projection %+v", status, progress)
	}

	visible, err := q.ListWorkTaskEntries(ctx, first.ID)
	if err != nil || !slices.Contains(visible, note) {
		t.Fatalf("visible ledger before retraction: %+v, %v", visible, err)
	}
	if _, err := svc.TombstoneEntry(ctx, worktask.Bot("assignee"), worktask.OriginUser, first.ID, note.Seq); err == nil {
		t.Fatal("a Bot retracted an entry")
	}
	tombstone, err := svc.TombstoneEntry(ctx, worktask.Owner, worktask.OriginUser, first.ID, note.Seq)
	if err != nil {
		t.Fatal(err)
	}
	var target int64
	if err := conn.QueryRowContext(ctx, `SELECT json_extract(meta_json, '$.targetSeq') FROM work_task_entries WHERE id = ?`, tombstone.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	afterTombstone := boardTask(t, q, first.ID)
	if tombstone.Seq != 4 || tombstone.Kind != "tombstone" || target != note.Seq ||
		afterTombstone.Status != progress.Status || afterTombstone.Revision != progress.Revision+1 || afterTombstone.NextEntrySeq != 5 {
		t.Fatalf("tombstone %+v, target %d; projection %+v", tombstone, target, afterTombstone)
	}
	if original := boardEntry(t, q, first.ID, note.Seq); original != note {
		t.Fatalf("retraction modified original entry: got %+v; want %+v", original, note)
	}
	visible, err = q.ListWorkTaskEntries(ctx, first.ID)
	if err != nil || !slices.Equal(visible, []sqlc.WorkTaskEntry{firstEntry}) {
		t.Fatalf("visible ledger after retraction: %+v, %v", visible, err)
	}
	if want := []int64{firstEntry.ID, secondEntry.ID, note.ID, status.ID, tombstone.ID}; !slices.Equal(published, want) {
		t.Errorf("notifications = %v; want %v", published, want)
	}
}

func TestUserTaskBoardReservesCancelAndReopenForOwner(t *testing.T) {
	_, q, svc := openBoardTest(t)
	ctx := t.Context()
	assignee := worktask.Bot("assignee")
	task := createBoardTestTask(t, svc, "Lifecycle control", "backlog", "")
	ready, err := svc.TransitionStatus(ctx, assignee, worktask.OriginTaskWake, task.ID, "ready", nil)
	if err != nil || ready.Status != "ready" {
		t.Fatalf("move backlog Task to ready: %+v, %v", ready, err)
	}

	for _, status := range []string{"cancelled", "blocked"} {
		if _, err := svc.TransitionStatus(ctx, assignee, worktask.OriginUser, task.ID, status, nil); err == nil {
			t.Errorf("assignee set status to %q", status)
		}
	}
	assertBoardUnchanged(t, q, ready)
	cancelled, err := svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, task.ID, "cancelled", nil)
	if err != nil || cancelled.Status != "cancelled" || boardTask(t, q, task.ID) != cancelled {
		t.Fatalf("Owner cancelled Task: %+v, %v", cancelled, err)
	}

	if _, err := svc.TransitionStatus(ctx, assignee, worktask.OriginUser, task.ID, "in_progress", nil); err == nil {
		t.Fatal("assignee reopened a cancelled Task")
	}
	assertBoardUnchanged(t, q, cancelled)
	reopened, err := svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, task.ID, "in_progress", nil)
	if err != nil || reopened.Status != "in_progress" || boardTask(t, q, task.ID) != reopened {
		t.Fatalf("Owner reopened Task: %+v, %v", reopened, err)
	}
}

func TestUserTaskBoardReviewBindsApprovalToLatestSubmission(t *testing.T) {
	_, q, svc := openBoardTest(t)
	ctx := t.Context()
	assignee, reviewer := worktask.Bot("assignee"), worktask.Bot("reviewer")
	task := createBoardTestTask(t, svc, "Review work", "in_progress", "reviewer")

	submit := func(before sqlc.WorkTask) sqlc.WorkTask {
		t.Helper()
		after, err := svc.TransitionStatus(t.Context(), assignee, worktask.OriginTaskWake, before.ID, "in_review", nil)
		if err != nil {
			t.Fatal(err)
		}
		entry := boardEntry(t, q, before.ID, before.NextEntrySeq)
		if after.Status != "in_review" || after.ReviewSeq != entry.Seq || entry.Kind != "status" ||
			after.Revision != before.Revision+1 || boardTask(t, q, before.ID) != after {
			t.Fatalf("review entry %+v; projection %+v", entry, after)
		}
		return after
	}
	first := submit(task)
	if _, err := svc.Review(ctx, assignee, worktask.OriginUser, task.ID, "approve", first.ReviewSeq, ""); err == nil {
		t.Fatal("the assignee approved their own work without being the reviewer")
	}
	assertBoardUnchanged(t, q, first)

	const feedback = "Include the failure case"
	changes, err := svc.Review(ctx, reviewer, worktask.OriginTaskWake, task.ID, "request_changes", first.ReviewSeq, feedback)
	if err != nil {
		t.Fatal(err)
	}
	entry := boardEntry(t, q, task.ID, first.NextEntrySeq)
	if changes.Status != "in_progress" || changes.Revision != first.Revision+1 || changes.NextEntrySeq != entry.Seq+1 ||
		entry.Kind != "review" || entry.Content != feedback || entry.ActorBotID != "reviewer" || boardTask(t, q, task.ID) != changes {
		t.Fatalf("change request entry %+v; projection %+v", entry, changes)
	}
	latest := submit(changes)
	if latest.ReviewSeq <= first.ReviewSeq {
		t.Fatalf("resubmission review seq = %d; want greater than %d", latest.ReviewSeq, first.ReviewSeq)
	}
	artifact, err := svc.PostEntry(ctx, assignee, worktask.OriginTaskWake, task.ID, worktask.PostEntryParams{
		Kind: "artifact", Content: "Test report",
	})
	if err != nil || artifact.Kind != "artifact" || artifact.Seq != latest.NextEntrySeq {
		t.Fatalf("posted artifact: %+v, %v", artifact, err)
	}
	withArtifact := boardTask(t, q, task.ID)
	if withArtifact.ReviewSeq != latest.ReviewSeq || withArtifact.Status != "in_review" || withArtifact.Revision != latest.Revision+1 {
		t.Fatalf("artifact changed review binding: before %+v; after %+v", latest, withArtifact)
	}
	if _, err := svc.Review(ctx, reviewer, worktask.OriginTaskWake, task.ID, "approve", first.ReviewSeq, ""); !errors.Is(err, worktask.ErrConflict) {
		t.Fatalf("approval of old submission: got %v; want ErrConflict", err)
	}
	assertBoardUnchanged(t, q, withArtifact)

	done, err := svc.Review(ctx, reviewer, worktask.OriginTaskWake, task.ID, "approve", latest.ReviewSeq, "")
	if err != nil {
		t.Fatal(err)
	}
	approval := boardEntry(t, q, task.ID, withArtifact.NextEntrySeq)
	if done.Status != "done" || done.Revision != withArtifact.Revision+1 || done.NextEntrySeq != approval.Seq+1 ||
		approval.Kind != "review" || approval.ActorBotID != "reviewer" || boardTask(t, q, task.ID) != done {
		t.Fatalf("approval entry %+v; projection %+v", approval, done)
	}
}

func TestUserTaskBoardDerivesBlockersAndMainQueuePosition(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()
	task := createBoardTestTask(t, svc, "Blocked Task", "in_progress", "")
	other := createBoardTestTask(t, svc, "Other Task", "ready", "")
	boardExec(t, conn, `INSERT INTO work_source_events (id, namespace, event_type, objects, revision, content, source_url,
		occurred_at, received_at, task_id, disposition) VALUES ('merged', 'github', 'merged', '[]', 'r1', '', '', 2, 2, ?, 'attached')`, task.ID)
	boardExec(t, conn, `INSERT INTO work_task_waits (id, task_id, namespace, object_type, object_key, event_type, revision, after_at, resolved_by)
		VALUES (10, ?, 'github', 'pull', '10', 'merged', 'r1', 1, NULL),
		       (11, ?, 'github', 'pull', '11', 'merged', 'r1', 1, 'merged'),
		       (12, ?, 'github', 'pull', '12', 'merged', 'r1', 1, NULL)`, task.ID, task.ID, other.ID)

	seedQuestion := func(id, taskID, botID, status, submissionStatus string, conversationID, queued int64) {
		t.Helper()
		boardExec(t, conn, `INSERT INTO submissions (id, conversation_id, kind, content, status, created_at)
			VALUES (?, ?, 'task_result', '', ?, 1)`, id, conversationID, submissionStatus)
		if taskID != "" {
			boardExec(t, conn, `INSERT INTO work_task_inputs (submission_id, task_id) VALUES (?, ?)`, id, taskID)
		}
		boardExec(t, conn, `INSERT INTO questions (id, agent_id, conversation_id, source_submission_id, tool_operation_key,
			text, options_json, status, queued, created_at) VALUES (?, ?, ?, ?, ?, 'Proceed?', '[]', ?, ?, 1)`,
			id, botID, conversationID, id, id, status, queued)
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
		blockers, err := worktask.ComputeBlockers(ctx, q, task)
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

	closed, err := svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, task.ID, worktask.StatusDone, nil)
	if err != nil {
		t.Fatal(err)
	}
	if blockers, err := worktask.ComputeBlockers(ctx, q, closed); err != nil || blockers != nil {
		t.Fatalf("closed Task blockers = %+v, %v; want nil, nil", blockers, err)
	}
	task, err = svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, task.ID, worktask.StatusInProgress, nil)
	if err != nil {
		t.Fatal(err)
	}
	boardExec(t, conn, `UPDATE work_task_waits SET resolved_by = 'merged' WHERE id = 10`)
	boardExec(t, conn, `UPDATE questions SET status = 'answered' WHERE id = 'first-in-queue'`)
	assertBlockers(0, map[string]int64{"second-in-queue": 2, "child-question": 0})
	boardExec(t, conn, `UPDATE questions SET status = 'answered' WHERE id IN ('second-in-queue', 'child-question')`)
	assertBlockers(0, nil)
	assertBoardUnchanged(t, q, task)
}

func openBoardTest(t *testing.T) (*sql.DB, *sqlc.Queries, *worktask.Service) {
	t.Helper()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	boardExec(t, conn, `INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES
		('assignee', 'Assignee', 'faux', 'echo', 1), ('reviewer', 'Reviewer', 'faux', 'echo', 1),
		('linked', 'Linked', 'faux', 'echo', 1), ('other', 'Other', 'faux', 'echo', 1)`)
	boardExec(t, conn, `INSERT INTO conversations (id, agent_id, channel_key, created_at, parent_id) VALUES
		(1, 'assignee', 'main', 1, NULL), (2, 'other', 'main', 1, NULL), (3, 'assignee', 'chat:child', 1, 1),
		(4, 'linked', 'chat:linked', 1, 1), (5, 'linked', 'chat:other-child', 1, 2),
		(6, 'assignee', 'chat:standalone', 1, NULL), (7, 'linked', 'main', 1, NULL),
		(8, 'linked', 'chat:standalone', 1, NULL)`)
	return conn, sqlc.New(conn), worktask.New(conn, nil)
}

func boardExec(t *testing.T, conn *sql.DB, stmt string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), stmt, args...); err != nil {
		t.Fatalf("execute %s: %v", stmt, err)
	}
}

func createBoardTestTask(t *testing.T, svc *worktask.Service, title, status, reviewerID string) sqlc.WorkTask {
	t.Helper()
	params := worktask.CreateTaskParams{AssigneeBotID: "assignee", Title: title, Status: status, Note: "Task brief"}
	if reviewerID != "" {
		params.ReviewerKind, params.ReviewerID = "bot", reviewerID
	}
	task, err := svc.CreateTask(t.Context(), worktask.Owner, worktask.OriginUser, params)
	if err != nil {
		t.Fatalf("create Task %q: %v", title, err)
	}
	return task
}

func boardTask(t *testing.T, q *sqlc.Queries, id string) sqlc.WorkTask {
	t.Helper()
	task, err := q.GetBoardTask(t.Context(), id)
	if err != nil {
		t.Fatalf("read Task %s: %v", id, err)
	}
	return task
}

func boardEntry(t *testing.T, q *sqlc.Queries, id string, seq int64) sqlc.WorkTaskEntry {
	t.Helper()
	entry, err := q.GetWorkTaskEntry(t.Context(), sqlc.GetWorkTaskEntryParams{TaskID: id, Seq: seq})
	if err != nil {
		t.Fatalf("read Task %s entry %d: %v", id, seq, err)
	}
	return entry
}

func assertBoardUnchanged(t *testing.T, q *sqlc.Queries, want sqlc.WorkTask) {
	t.Helper()
	if got := boardTask(t, q, want.ID); got != want {
		t.Errorf("Task projection changed: got %+v; want %+v", got, want)
	}
	if entry, err := q.GetWorkTaskEntry(t.Context(), sqlc.GetWorkTaskEntryParams{TaskID: want.ID, Seq: want.NextEntrySeq}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("next ledger sequence %d should be unused: got %+v, %v", want.NextEntrySeq, entry, err)
	}
}
