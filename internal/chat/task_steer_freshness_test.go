package chat

import (
	"encoding/json/v2"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserTaskWithUnverifiedSteerDoesNotClaimGoalMet(t *testing.T) {
	for _, state := range []string{"pending", "reconciliation"} {
		t.Run(state, func(t *testing.T) {
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
			data, err := json.Marshal(engine.TaskReport{Outcome: "done", Summary: "earlier report"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.q.StageTaskReport(t.Context(), sqlc.StageTaskReportParams{ID: task.ID, ConversationID: row.ConversationID, Report: string(data)}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, "steer", "new requirement"); err != nil {
				t.Fatal(err)
			}
			if state == "reconciliation" {
				if _, err := r.conn.ExecContext(t.Context(), `UPDATE task_steer_receipts SET state = 'reconciliation' WHERE task_id = ?`, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.service.settleTask(t.Context(), task.ID, engine.Outcome{Status: "done"}); err != nil {
				t.Fatal(err)
			}
			saved, err := r.service.GetTask(t.Context(), "bot", parent, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.GoalMet {
				t.Fatal("task claims an unverified steer was covered")
			}
			var payload struct {
				Result struct {
					GoalMet bool `json:"goalMet"`
				} `json:"task-result"`
			}
			if err := json.Unmarshal([]byte(receipt(t, r, "task:"+task.ID+":result").Content), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Result.GoalMet {
				t.Fatal("parent result claims an unverified steer was covered")
			}
		})
	}
}
