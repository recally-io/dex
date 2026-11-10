package chat

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserTaskResultReportsUndeliveredSteer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		steer  bool
		long   bool
		status string
	}{
		{"success", true, false, "done"}, {"long report", true, true, "done"}, {"no steer", false, false, "done"}, {"failed", true, false, "unanswered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := startNative(t, "echo")
			parent, source := taskParent(t, r)
			task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
			if err != nil {
				t.Fatal(err)
			}
			summary := "original worker success"
			if tc.long {
				summary += strings.Repeat("x", 6000)
			}
			original := engine.TaskReport{Outcome: "done", Summary: summary}
			data, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			row, err := r.q.GetTask(t.Context(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.q.StageTaskReport(t.Context(), sqlc.StageTaskReportParams{ID: task.ID, ConversationID: row.ConversationID, Report: string(data)}); err != nil {
				t.Fatal(err)
			}
			if tc.steer {
				if _, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, "steer", "secret steer text"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.service.settleTask(t.Context(), task.ID, engine.Outcome{Status: tc.status, Answer: "answer", Reason: "execution failed"}); err != nil {
				t.Fatal(err)
			}
			saved, err := r.service.GetTask(t.Context(), "bot", parent, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.StructuredReport == nil || saved.StructuredReport.Summary != summary || saved.StructuredReport.Outcome != "done" {
				t.Fatalf("structured report changed: %+v", saved.StructuredReport)
			}
			var payload struct {
				Result struct {
					Report string `json:"report"`
				} `json:"task-result"`
			}
			if err := json.Unmarshal([]byte(receipt(t, r, "task:"+task.ID+":result").Content), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Result.Report != truncateTaskReport(saved.Report) {
				t.Fatal("parent result differs from saved report")
			}
			if tc.steer {
				state, reason, err := r.service.TaskSteerReceipt(t.Context(), source, "steer")
				if err != nil || state != "undelivered" || reason == "" {
					t.Fatalf("receipt: %q %q %v", state, reason, err)
				}
				for _, text := range []string{saved.Report, payload.Result.Report} {
					if !strings.Contains(text, "undelivered") || !strings.Contains(text, reason) || strings.Contains(text, "secret steer text") {
						t.Fatalf("bad report: %s", text)
					}
				}
			} else if saved.Report != string(data) {
				t.Fatalf("report changed without steer: %s", saved.Report)
			}
		})
	}
}

func TestUserTaskResultDoesNotMisclassifyReconciliation(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"uncertain", "unsent"} {
		if _, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, id, "private"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.conn.ExecContext(t.Context(), `UPDATE task_steer_receipts SET state = 'reconciliation' WHERE request_id = 'uncertain'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.settleTask(t.Context(), task.ID, engine.Outcome{Status: "done", Answer: "worker report"}); err != nil {
		t.Fatal(err)
	}
	state, reason, err := r.service.TaskSteerReceipt(t.Context(), source, "uncertain")
	if err != nil || state != "reconciliation" || reason != "" {
		t.Fatalf("uncertain receipt: %s %s %v", state, reason, err)
	}
	state, reason, err = r.service.TaskSteerReceipt(t.Context(), source, "unsent")
	if err != nil || state != "undelivered" || reason == "" {
		t.Fatalf("unsent receipt: %s %s %v", state, reason, err)
	}
	saved, err := r.service.GetTask(t.Context(), "bot", parent, task.ID)
	if err != nil || !strings.Contains(saved.Report, reason) {
		t.Fatalf("report: %s %v", saved.Report, err)
	}
}

func TestUserToolCancellationRetainsStructuredReportWithUndeliveredSteer(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	original := engine.TaskReport{Outcome: "done", Summary: "staged report"}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.StageTaskReport(t.Context(), sqlc.StageTaskReportParams{ID: task.ID, ConversationID: row.ConversationID, Report: string(data)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, "steer", "private"); err != nil {
		t.Fatal(err)
	}
	cancelled, err := r.service.ToolCancelTask(t.Context(), source, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.service.GetTask(t.Context(), "bot", parent, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Report != saved.Report || !strings.Contains(cancelled.Report, "undelivered") {
		t.Fatalf("first cancellation report %q differs from saved report %q", cancelled.Report, saved.Report)
	}
	for _, view := range []Task{cancelled, saved} {
		if view.Status != "cancelled" || view.GoalMet || !view.HasStructuredReport || view.StructuredReport == nil || view.StructuredReport.Summary != original.Summary || view.StructuredReport.Outcome != original.Outcome {
			t.Fatalf("cancellation changed structured report semantics: %+v", view)
		}
	}
	retry, err := r.service.ToolCancelTask(t.Context(), source, task.ID)
	if err != nil || retry.Report != saved.Report {
		t.Fatalf("repeated cancellation report %q, error %v", retry.Report, err)
	}
}

func TestUserCancelledTaskReportsUndeliveredSteerWithoutResultTurn(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, "steer", "private"); err != nil {
		t.Fatal(err)
	}
	cancelled, err := r.service.CancelTask(t.Context(), "bot", parent, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, reason, err := r.service.TaskSteerReceipt(t.Context(), source, "steer")
	if err != nil || state != "undelivered" || reason == "" {
		t.Fatalf("receipt: %s %s %v", state, reason, err)
	}
	saved, err := r.service.GetTask(t.Context(), "bot", parent, task.ID)
	if err != nil || !strings.Contains(saved.Report, reason) || !strings.Contains(saved.Report, "undelivered") {
		t.Fatalf("report: %s %v", saved.Report, err)
	}
	if cancelled.Report != saved.Report {
		t.Fatalf("first cancellation report %q differs from saved report %q", cancelled.Report, saved.Report)
	}
	var count int
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE id = ?`, "task:"+task.ID+":result").Scan(&count); err != nil || count != 0 {
		t.Fatalf("result count: %d %v", count, err)
	}
}
