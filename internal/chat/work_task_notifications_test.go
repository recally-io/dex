package chat

import (
	"database/sql"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserReceivesTaskQuestionsAtPrimaryTarget(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	convID, _ := taskParent(t, r)
	conv, err := r.q.GetConversation(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.q.InsertRoutineNotificationTarget(ctx, sqlc.InsertRoutineNotificationTargetParams{
		ID: "target", AgentID: conv.AgentID, Platform: "telegram", AccountID: "123", Peer: "telegram:42",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.SetPrimaryNotificationTarget(ctx, sqlc.SetPrimaryNotificationTargetParams{
		ID: conv.AgentID, PrimaryNotificationTargetID: sql.NullString{String: "target", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	task, err := r.q.CreateWorkTask(ctx, sqlc.CreateWorkTaskParams{ID: "task", AgentID: conv.AgentID, Title: "Sign-in", At: 1})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id, origin, exit, want string
		linked                 bool
	}{
		{"task-web", "web", "ask_user", "telegram:42", true},
		{"task-channel", "telegram:17", "ask_user", "telegram:17", true},
		{"ordinary-question", "web", "ask_user", "", false},
		{"ordinary-reply", "web", "", "", true},
	}
	for _, tc := range cases {
		_, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: tc.id, ConversationID: conv.ID, Kind: "message", Origin: tc.origin, Content: "input", Attachments: "null", CreatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if tc.linked {
			if err := r.q.AssociateWorkTaskInput(ctx, sqlc.AssociateWorkTaskInputParams{SubmissionID: tc.id, TaskID: task.ID}); err != nil {
				t.Fatal(err)
			}
		}
		sub, err := r.q.GetSubmission(ctx, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		sub.ExitAction = tc.exit
		peer, err := writeOutboxOnSettle(ctx, r.q, sub, conv, engine.Outcome{Status: "done", Answer: "Choose one."}, 2)
		if err != nil || peer != tc.want {
			t.Fatalf("%s: peer=%q, want %q; %v", tc.id, peer, tc.want, err)
		}
	}
}
