package tools

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	tasksvc "github.com/recally-io/dex/internal/task"
)

// TestUserMainClosesTaskOnNativeChildResult checks the Task closeout on the turn of a native child result.
// Main's faux model cannot call a tool from a session-result input, so this test uses the real tool service
// and SQLite. The native result comes from the real writer of child turn results.
func TestUserMainClosesTaskOnNativeChildResult(t *testing.T) {
	t.Parallel()
	s, conn := openTools(t)
	ctx := t.Context()
	q := sqlc.New(conn)

	// Given child Sessions of the assignee Main, the reviewer Main, and an assignee top-level Session,
	// each with a settled turn whose native result goes to its parent.
	if _, err := conn.ExecContext(ctx, `INSERT INTO sessions (id, bot_id, channel_key, execution_id, created_at, parent_id) VALUES
		(7, 'assignee', 'chat:worker', 7, 1, 1), (8, 'reviewer', 'chat:worker', 8, 1, 2), (9, 'assignee', 'chat:worker-2', 9, 1, 5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, session_id, content, status, created_at, kind, origin, producer, parent_request_id) VALUES
		('worker-turn', 7, 'x', 'done', 1, 'message', 'task', 'sub_session', 'user'),
		('reviewer-turn', 8, 'x', 'done', 1, 'message', 'task', 'sub_session', 'reviewer-user'),
		('session-turn', 9, 'x', 'done', 1, 'message', 'task', 'sub_session', 'session-user'),
		('rejected-turn', 7, 'x', 'unanswered', 1, 'message', 'task', 'sub_session', 'user')`); err != nil {
		t.Fatal(err)
	}
	for i, turn := range []string{"worker-turn", "reviewer-turn", "session-turn"} {
		if n, err := q.CreateTurnResult(ctx, sqlc.CreateTurnResultParams{
			ID: "result:" + turn, TurnID: turn, Content: `{"session-result":{}}`, CreatedAt: 2,
			NativeAnswerID: sql.NullInt64{Int64: int64(i + 1), Valid: true},
		}); err != nil || n != 1 {
			t.Fatalf("native result of %s: %d, %v", turn, n, err)
		}
	}
	// And a child turn that ended without a native answer, such as a permanent admission rejection.
	// Its result has the child in result_child_id but no native answer.
	if n, err := q.CreateTurnResult(ctx, sqlc.CreateTurnResultParams{
		ID: "result:rejected-turn", TurnID: "rejected-turn", Content: `{"session-result":{"outcome":"failed"}}`, CreatedAt: 2,
	}); err != nil || n != 1 {
		t.Fatalf("answerless result: %d, %v", n, err)
	}
	// And other Main inputs that are not native results: a session_report, a child failure notice,
	// a child message, and a plugin input that copies the native result field.
	if _, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, session_id, content, status, created_at, kind, origin, producer, parent_request_id, result_child_id) VALUES
		('report:worker', 1, 'x', 'done', 1, 'session_result', 'web', 'sub_session', NULL, NULL),
		('err:worker', 1, 'x', 'done', 1, 'session_result', 'web', 'sub_session', NULL, NULL),
		('child-message', 1, 'x', 'done', 1, 'message', 'task', 'sub_session', 'user', NULL),
		('copied-result', 1, 'x', 'done', 1, 'session_result', 'task', 'plugin', NULL, 7)`); err != nil {
		t.Fatal(err)
	}

	// And an in-progress Task with no Reviewer, an in-progress Task with a Bot Reviewer, and a ready Task.
	create := func(title, status, reviewer string) string {
		t.Helper()
		p := tasksvc.CreateTaskParams{AssigneeBotID: "assignee", Title: title, Status: status}
		if reviewer != "" {
			p.ReviewerKind, p.ReviewerID = tasksvc.ActorBot, reviewer
		}
		task, err := s.tasks.CreateTask(ctx, tasksvc.Owner, tasksvc.OriginUser, p)
		if err != nil {
			t.Fatal(err)
		}
		return tasksvc.FormatKey(task.Number)
	}
	solo := create("Ship the fix", tasksvc.StatusInProgress, "")
	reviewed := create("Ship the reviewed fix", tasksvc.StatusInProgress, "reviewer")
	ready := create("Start later", tasksvc.StatusReady, "")
	status := func(key string) string {
		t.Helper()
		task, err := tasksvc.Lookup(ctx, q, key)
		if err != nil {
			t.Fatal(err)
		}
		return task.Status
	}
	main := func(source, key string) engine.TaskContext { return toolCall("assignee", 1, source, key) }
	native := func(key string) engine.TaskContext { return main("result:worker-turn", key) }
	update := func(c engine.TaskContext, a TaskUpdateArgs) error { _, err := s.Update(ctx, c, a); return err }

	// When other sources, other roles, or other changes use a native result turn, Then dex refuses them.
	for _, tc := range []struct {
		name string
		want error
		call func() error
	}{
		{"done from answerless child result", tasksvc.ErrForbidden, func() error {
			return update(main("result:rejected-turn", "rejected-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"in_review from answerless child result", tasksvc.ErrForbidden, func() error {
			return update(main("result:rejected-turn", "rejected-review"), TaskUpdateArgs{ID: reviewed, Status: "in_review"})
		}},
		{"done from session_report", tasksvc.ErrForbidden, func() error {
			return update(main("report:worker", "report-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"in_review from session_report", tasksvc.ErrForbidden, func() error {
			return update(main("report:worker", "report-review"), TaskUpdateArgs{ID: reviewed, Status: "in_review"})
		}},
		{"done from child failure notice", tasksvc.ErrForbidden, func() error {
			return update(main("err:worker", "err-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"done from child message", tasksvc.ErrForbidden, func() error {
			return update(main("child-message", "message-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"done from copied result field", tasksvc.ErrForbidden, func() error {
			return update(main("copied-result", "copied-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"done from plugin alert", tasksvc.ErrForbidden, func() error {
			return update(main("alert", "alert-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"done from Monitor or Process wake", tasksvc.ErrForbidden, func() error {
			return update(main("system-wake", "wake-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"in_review by reviewer Main", tasksvc.ErrForbidden, func() error {
			return update(toolCall("reviewer", 2, "result:reviewer-turn", "reviewer-submit"), TaskUpdateArgs{ID: reviewed, Status: "in_review"})
		}},
		{"done by assignee top-level Session", tasksvc.ErrForbidden, func() error {
			return update(toolCall("assignee", 5, "result:session-turn", "session-done"), TaskUpdateArgs{ID: solo, Status: "done"})
		}},
		{"start", tasksvc.ErrForbidden, func() error { return update(native("start"), TaskUpdateArgs{ID: ready, Status: "in_progress"}) }},
		{"move to ready", tasksvc.ErrForbidden, func() error { return update(native("ready"), TaskUpdateArgs{ID: solo, Status: "ready"}) }},
		{"move to backlog", tasksvc.ErrForbidden, func() error { return update(native("backlog"), TaskUpdateArgs{ID: solo, Status: "backlog"}) }},
		{"cancel", tasksvc.ErrForbidden, func() error { return update(native("cancel"), TaskUpdateArgs{ID: solo, Status: "cancelled"}) }},
		{"rename", tasksvc.ErrForbidden, func() error { return update(native("rename"), TaskUpdateArgs{ID: solo, Title: "Renamed"}) }},
		{"link session", tasksvc.ErrForbidden, func() error { return update(native("link"), TaskUpdateArgs{ID: solo, Session: "7"}) }},
		{"done with rename", tasksvc.ErrForbidden, func() error {
			return update(native("done-rename"), TaskUpdateArgs{ID: solo, Status: "done", Title: "Renamed"})
		}},
		// task_assign reports a refused handoff as an invalid call.
		{"assign", ErrInvalid, func() error {
			_, err := s.Assign(ctx, native("assign"), TaskAssignArgs{ID: solo, AssigneeBotID: "other"})
			return err
		}},
		{"split", tasksvc.ErrForbidden, func() error {
			_, err := s.Split(ctx, native("split"), TaskSplitArgs{ParentID: solo, Title: "Part"})
			return err
		}},
		{"create", tasksvc.ErrForbidden, func() error { _, err := s.New(ctx, native("new"), TaskNewArgs{Title: "New"}); return err }},
	} {
		if err := tc.call(); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := []string{status(solo), status(reviewed), status(ready)}; got[0] != "in_progress" || got[1] != "in_progress" || got[2] != "ready" {
		t.Fatalf("refused calls changed status: %v", got)
	}
	if task, err := tasksvc.Lookup(ctx, q, solo); err != nil || task.AssigneeBotID != "assignee" || task.Title != "Ship the fix" {
		t.Fatalf("refused calls changed the Task: %+v, %v", task, err)
	}
	// And an answerless child result can still post a note and link an object, as before.
	rejected := main("result:rejected-turn", "rejected-post")
	if _, err := s.Post(ctx, rejected, TaskPostArgs{ID: solo, Content: "Child was rejected"}); err != nil {
		t.Errorf("post from answerless child result: %v", err)
	}
	rejected.Key += "/link"
	pull := tasksvc.Link{Namespace: "fixture", Name: "PR", Object: tasksvc.Object{Type: "pull", Key: "9"}}
	if _, err := s.Link(ctx, rejected, "link-fixture", solo, pull); err != nil {
		t.Errorf("link from answerless child result: %v", err)
	}

	// When the assignee Main closes out on the native result turn,
	// Then a Task with no Reviewer goes to done, and a native result cannot reopen it.
	if done, err := s.Update(ctx, native("solo-done"), TaskUpdateArgs{ID: solo, Status: "done"}); err != nil || done.Status != "done" {
		t.Errorf("done from a native result: %+v, %v", done, err)
	} else if err := update(native("reopen"), TaskUpdateArgs{ID: solo, Status: "in_progress"}); !errors.Is(err, tasksvc.ErrForbidden) {
		t.Errorf("reopen from a native result: %v", err)
	}

	// And a Task with a Reviewer cannot go to done, and it can go to in_review.
	if err := update(native("reviewed-done"), TaskUpdateArgs{ID: reviewed, Status: "done"}); !errors.Is(err, tasksvc.ErrInvalid) {
		t.Errorf("done without its Reviewer from a native result: %v, status %s", err, status(reviewed))
	}
	submitted, err := s.Update(ctx, native("reviewed-submit"), TaskUpdateArgs{ID: reviewed, Status: "in_review"})
	if err != nil || submitted.Status != "in_review" || submitted.ReviewSeq == 0 {
		t.Fatalf("submit for review from a native result: %+v, %v", submitted, err)
	}
	if err := update(native("reviewed-done-after"), TaskUpdateArgs{ID: reviewed, Status: "done"}); !errors.Is(err, tasksvc.ErrInvalid) {
		t.Fatalf("done during review from a native result: %v", err)
	}
	approve := TaskReviewArgs{ID: reviewed, Decision: "approve", ReviewSeq: submitted.ReviewSeq}
	if _, err := s.Review(ctx, toolCall("reviewer", 2, "result:reviewer-turn", "reviewer-approve"), approve); !errors.Is(err, tasksvc.ErrForbidden) {
		t.Fatalf("approval from a native result: %v", err)
	}
	if got := status(reviewed); got != "in_review" {
		t.Fatalf("reviewed Task status = %s, want in_review", got)
	}

	// And a native result can still post a note, as before.
	if _, err := s.Post(ctx, native("post"), TaskPostArgs{ID: solo, Content: "Child result checked"}); err != nil {
		t.Fatalf("post from a native result: %v", err)
	}
}
