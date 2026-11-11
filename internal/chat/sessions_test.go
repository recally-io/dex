package chat

import (
	"database/sql"
	"errors"
	"strconv"
	"sync"
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

func TestUserSessionNewReplayKeepsTheSavedDirectory(t *testing.T) {
	// Given a call that created a child in project A.
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	a, b := t.TempDir(), t.TempDir()
	first, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "One", Message: "one", Cwd: a})
	if err != nil {
		t.Fatal(err)
	}
	// When the parent switches to project B and the same call runs again.
	if _, err := r.service.SetProject(t.Context(), "bot", parent, b); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{"", b} {
		replay, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "One", Message: "one", Cwd: cwd})
		if err != nil {
			t.Fatal(err)
		}
		if replay.ID != first.ID {
			t.Fatalf("replay with cwd %q made another child: %+v", cwd, replay)
		}
	}
	// Then the child keeps project A, and no second child exists.
	id, err := strconv.ParseInt(first.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	child, err := r.q.GetConversation(t.Context(), id)
	if err != nil || child.Cwd != a {
		t.Fatalf("child cwd = %q, want %q (%v)", child.Cwd, a, err)
	}
	children, err := r.q.ListSessions(t.Context(), sqlc.ListSessionsParams{AgentID: "bot", ParentID: sql.NullInt64{Int64: parent, Valid: true}, MaxRows: 20})
	if err != nil || len(children) != 1 {
		t.Fatalf("children: %+v, %v", children, err)
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
	if _, err := r.service.ToolSessionList(t.Context(), asChild, engine.SessionListArgs{Scope: "all"}); err != nil {
		t.Fatalf("child list: %v", err)
	}
}

func TestUserSessionStopReplayKeepsLaterWork(t *testing.T) {
	// Given a child that the parent stopped, and work that the parent sent to it afterwards.
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Child", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	stop := source
	stop.Key = "bot/stop-one"
	first, err := r.service.ToolSessionStop(t.Context(), stop, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	send := source
	send.Key = "bot/send-after-stop"
	if _, err = r.service.ToolSessionSend(t.Context(), send, engine.SessionSendArgs{ID: child.ID, Message: "later work"}); err != nil {
		t.Fatal(err)
	}
	// When the same session_stop call runs again.
	replay, err := r.service.ToolSessionStop(t.Context(), stop, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Then it returns the stored result and the later work stays pending.
	if replay != first {
		t.Fatalf("replay returned %+v, the first call returned %+v", replay, first)
	}
	if turn := receipt(t, r, turnID(send.Key)); turn.Status != "pending" {
		t.Fatalf("a replayed stop withdrew later input: status %s, reason %s", turn.Status, turn.Reason.String)
	}
}

func TestUserConcurrentSessionForkWithOneKeyCreatesOneFork(t *testing.T) {
	// Given a child that has a finished turn.
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Child", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.service.admit(t.Context(), taskInput(t, r, turnID(source.Key))); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, turnID(source.Key))
	// When eight calls with the same key fork it at once.
	call := source
	call.Key = "bot/fork-one"
	start := make(chan struct{})
	refs := make(chan engine.SessionRef, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			<-start
			ref, err := r.service.ToolSessionFork(t.Context(), call, engine.SessionForkArgs{ID: child.ID, Message: "fork work"})
			refs <- ref
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(refs)
	close(errs)
	// Then every call succeeds with the same fork, and the parent has the source child and one fork.
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]bool{}
	for ref := range refs {
		ids[ref.ID] = true
	}
	if len(ids) != 1 {
		t.Fatalf("the calls returned %d different forks: %v", len(ids), ids)
	}
	var count int
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE parent_id = ?`, parent).Scan(&count); err != nil || count != 2 {
		t.Fatalf("children of the parent: %d, %v", count, err)
	}
}
