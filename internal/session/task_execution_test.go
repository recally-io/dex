package session

import (
	"strconv"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func TestUserTaskChildExecutionSettlesWithoutTaskWake(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state, receiptStatus, reason string
		results                            int
	}{
		{name: "completed", state: "completed", receiptStatus: "done", results: 1},
		{name: "failed", state: "failed", receiptStatus: "unanswered", reason: "model_error", results: 1},
		{name: "stopped before admission", state: "cancelled", receiptStatus: "unanswered", reason: "stopped"},
		{name: "withdrawn before admission", state: "cancelled", receiptStatus: "unanswered", reason: "withdrawn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a Task that Main delegates to a child.
			ctx := t.Context()
			r := startNative(t, "echo")
			parent, source := taskParent(t, r)
			task, err := tasksvc.New(r.conn, nil).CreateTask(ctx, tasksvc.Owner, tasksvc.OriginUser, tasksvc.CreateTaskParams{AssigneeBotID: "bot", Title: "Prepare draft"})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.q.AssociateTaskInput(ctx, sqlc.AssociateTaskInputParams{SubmissionID: source.SourceRequestID, TaskID: task.ID}); err != nil {
				t.Fatal(err)
			}
			args := NewArgs{Title: "Draft", Message: "Prepare draft"}
			if tc.reason == "model_error" {
				// The faux mixed model rejects the non-JSON message.
				args.Provider, args.ModelID = "faux", "mixed"
			}
			child, err := r.service.ToolSessionNew(ctx, source, args)
			if err != nil {
				t.Fatal(err)
			}
			childID, err := strconv.ParseInt(child.ID, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			turn := turnID(source.Key)
			getExecution := func() sqlc.TaskSession {
				t.Helper()
				row, err := r.q.GetTaskSession(ctx, sqlc.GetTaskSessionParams{TaskID: task.ID, SessionID: childID})
				if err != nil {
					t.Fatal(err)
				}
				return row
			}
			if row := getExecution(); row.TurnRequestID != turn || row.State != "running" || row.EndedAt.Valid {
				t.Errorf("new execution: turn=%q state=%q ended=%v, want %q running and no end time", row.TurnRequestID, row.State, row.EndedAt, turn)
			}
			countEntries := func() int {
				t.Helper()
				var n int
				if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM task_entries WHERE task_id = ?`, task.ID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := countEntries()

			// When the child finishes, fails, or is stopped or withdrawn before admission.
			switch tc.reason {
			case "stopped":
				if err := r.service.CancelSession(ctx, "bot", childID); err != nil {
					t.Fatal(err)
				}
			case "withdrawn":
				if _, err := r.service.Withdraw(ctx, "bot", childID, turn); err != nil {
					t.Fatal(err)
				}
			default:
				if err := r.service.admit(ctx, taskInput(t, r, turn)); err != nil {
					t.Fatal(err)
				}
			}
			sub := awaitReceipt(t, r, turn)
			if sub.Status != tc.receiptStatus || sub.Reason.String != tc.reason {
				t.Fatalf("child receipt = %s (%s), want %s (%s)", sub.Status, sub.Reason.String, tc.receiptStatus, tc.reason)
			}
			if replay, err := r.service.ToolSessionNew(ctx, source, args); err != nil || replay.ID != child.ID {
				t.Fatalf("session_new replay: %+v, %v", replay, err)
			}

			// A later parent turn in the same child cannot replace the first turn's outcome.
			if sub.Status == "done" {
				followUp := source
				followUp.Key = "bot/send-2"
				if _, err := r.service.ToolSessionSend(ctx, followUp, SendArgs{ID: child.ID, Message: "Revise draft"}); err != nil {
					t.Fatal(err)
				}
				if err := r.service.CancelSession(ctx, "bot", childID); err != nil {
					t.Fatal(err)
				}
				if later := receipt(t, r, turnID(followUp.Key)); later.Reason.String != "stopped" {
					t.Fatalf("later receipt = %+v, want stopped", later)
				}
			}

			// Then the execution keeps its first turn link and the receipt's end time.
			if row := getExecution(); row.TurnRequestID != turn || row.State != tc.state || !row.EndedAt.Valid || row.EndedAt != sub.SettledAt {
				t.Errorf("settled execution: turn=%q state=%q ended=%v, want %q %s %v", row.TurnRequestID, row.State, row.EndedAt, turn, tc.state, sub.SettledAt)
			}
			if after := countEntries(); after != before {
				t.Errorf("settlement or replay wrote %d Task ledger entries", after-before)
			}
			var results, wakes int
			err = r.conn.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE kind = 'session_result' AND session_id = ?), count(*) FILTER (WHERE kind = 'task_wake') FROM submissions`, parent).Scan(&results, &wakes)
			if err != nil || results != tc.results || wakes != 0 {
				t.Fatalf("parent got %d child results and %d Task wakes, want %d and 0 (query: %v)", results, wakes, tc.results, err)
			}
		})
	}
}
