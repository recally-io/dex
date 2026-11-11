package chat

import (
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func taskParent(t *testing.T, r *nativeTest) (int64, engine.TaskContext) {
	t.Helper()
	c, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.service.execution(t.Context(), c.ID, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "source", ConversationID: c.ID, Content: "delegate", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID, engine.TaskContext{AgentID: "bot", ConversationID: ref.ID, SourceRequestID: "source", Key: "bot/spawn-1"}
}

func taskInput(t *testing.T, r *nativeTest, id string) sqlc.ListPendingSubmissionsRow {
	t.Helper()
	rows, err := r.q.ListPendingSubmissions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing pending input %s", id)
	return sqlc.ListPendingSubmissionsRow{}
}

func TestUserSessionNewReplayKeepsIdentity(t *testing.T) {
	// Given a parent run that calls session_new.
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	first, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "One", Message: "one"})
	if err != nil {
		t.Fatal(err)
	}
	// When the same tool call runs again with other arguments.
	replay, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Replay", Message: "different"})
	if err != nil {
		t.Fatal(err)
	}
	// Then the first child stays, with its own turn.
	if replay.ID != first.ID || replay.Title != "One" {
		t.Fatalf("replay changed the child: %+v, %+v", first, replay)
	}
	children, err := r.q.ListSessions(t.Context(), sqlc.ListSessionsParams{AgentID: "bot", ParentID: sql.NullInt64{Int64: parent, Valid: true}, MaxRows: 20})
	if err != nil || len(children) != 1 {
		t.Fatalf("children: %+v, %v", children, err)
	}
	turn, err := r.q.GetTurn(t.Context(), turnID(source.Key))
	if err != nil || turn.Content != "one" || turn.Origin != "task" {
		t.Fatalf("turn: %+v, %v", turn, err)
	}
	link, err := r.q.GetTurnParent(t.Context(), turn.ID)
	if err != nil || link.ParentRequestID.String != "source" || link.ParentID.Int64 != parent {
		t.Fatalf("parent link: %+v, %v", link, err)
	}
}

func TestUserSessionToolsStayWithinTheParent(t *testing.T) {
	// Given a child of bot and a session of another bot.
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "One", Message: "one"})
	if err != nil {
		t.Fatal(err)
	}
	warmOtherAgent(t, r)
	other, err := r.service.Main(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	// When another bot writes to the child, the child is not found.
	otherRef, err := r.service.execution(t.Context(), other.ID, "other", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "other-source", ConversationID: other.ID, Content: "go", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	foreign := engine.TaskContext{AgentID: "other", ConversationID: otherRef.ID, SourceRequestID: "other-source", Key: "other/send"}
	if _, err := r.service.ToolSessionSend(t.Context(), foreign, engine.SessionSendArgs{ID: child.ID, Message: "hi"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("foreign send: %v", err)
	}
	if _, err := r.service.ToolSessionRead(t.Context(), foreign, engine.SessionReadArgs{ID: child.ID}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	// When the child calls a management tool, the handler rejects it, whatever tools the child has.
	childID, err := strconv.ParseInt(child.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := r.q.GetConversation(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	childRef, err := r.service.execution(t.Context(), conv.ID, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	asChild := engine.TaskContext{AgentID: "bot", ConversationID: childRef.ID, SourceRequestID: turnID(source.Key), Key: "bot/child-new"}
	if _, err := r.service.ToolSessionNew(t.Context(), asChild, engine.SessionNewArgs{Title: "Grandchild", Message: "no"}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("child new: %v", err)
	}
	if _, err := r.service.ToolSessionSend(t.Context(), asChild, engine.SessionSendArgs{ID: child.ID, Message: "no"}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("child send: %v", err)
	}
	// A child may read.
	if _, err := r.service.ToolSessionList(t.Context(), asChild, "all"); err != nil {
		t.Fatalf("child list: %v", err)
	}
}
