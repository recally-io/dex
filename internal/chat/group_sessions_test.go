package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserGroupChildInheritsAudienceAndInitiatingActor(t *testing.T) {
	// Given a paired group audience and a persisted input that owns this child call.
	r := startNative(t, "echo")
	groupSettings(t, r, "bot", "123:synthetic", "42")
	identity := ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7, MessageID: "1"}
	parent, _ := groupInput(t, r, "bot", "group-parent-context", identity, "public parent context")
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "group-parent-run", ConversationID: parent.ID, Content: "delegate public work", Attachments: "null", Origin: "telegram:-100:7", Kind: "message", ChannelContext: string(encoded)}); err != nil {
		t.Fatal(err)
	}
	caller := engine.TaskContext{AgentID: "bot", Scope: parent.ExecutionScope, ConversationID: parent.ExecutionID.Int64, SourceRequestID: "group-parent-run", Key: "bot/" + parent.ExecutionScope + "/group-child"}
	// When the group delegates one child, the existing parent-turn/native engine path must retain scope.
	delegated, err := r.service.ToolGroupSession(t.Context(), caller, engine.GroupSessionArgs{Action: "start", Title: "Public group child", Message: "public child work"})
	if err != nil || delegated.Session == nil {
		t.Fatalf("same-audience child denied: %v", err)
	}
	id, err := strconv.ParseInt(delegated.Session.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := r.q.GetConversation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentID != parent.AgentID || conv.ExecutionScope != parent.ExecutionScope || conv.ScopeContext != parent.ScopeContext || conv.Cwd != "" || !conv.ParentID.Valid || conv.ParentID.Int64 != parent.ID {
		t.Fatalf("child upgraded its resource ownership: %+v", conv)
	}
	turn := turnID(caller.Key)
	if err := r.service.admit(t.Context(), taskInput(t, r, turn)); err != nil {
		t.Fatal(err)
	}
	got := awaitReceipt(t, r, turn)
	if got.Status != "done" || got.Answer.String != "echo: public child work" {
		t.Fatalf("child could not execute in the audience: %+v", got)
	}
	// Then its result retains the original group origin and actual source actor.
	var resultContext, origin string
	if err := r.conn.QueryRowContext(t.Context(), "SELECT channel_context,origin FROM submissions WHERE result_child_id=? AND kind='task_result'", id).Scan(&resultContext, &origin); err != nil {
		t.Fatal(err)
	}
	if resultContext != string(encoded) || origin != "telegram:-100:7" {
		t.Fatalf("child result lost actor/audience: context=%s origin=%s", resultContext, origin)
	}
	// Cross-bot settings, private projects and nested child creation remain unavailable.
	for _, args := range []engine.SessionNewArgs{{Title: "Bad", Message: "x", Bot: "other"}, {Title: "Bad", Message: "x", Cwd: r.dir}, {Title: "Bad", Message: "x", Provider: "faux", ModelID: "bash"}} {
		if _, err := r.service.newSession(t.Context(), caller, args); err == nil {
			t.Fatalf("group child accepted private override: %+v", args)
		}
	}
	conv, err = r.q.GetConversation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	nested := caller
	nested.ConversationID = conv.ExecutionID.Int64
	if _, err := r.q.CreateParentTurn(t.Context(), sqlc.CreateParentTurnParams{ID: "nested-group-source", ConversationID: id, Content: "nested source", WhenBusy: "followUp", ParentRequestID: sql.NullString{String: "group-parent-run", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	nested.SourceRequestID = "nested-group-source"
	nested.Key = "bot/" + parent.ExecutionScope + "/nested"
	if _, err := r.service.ToolGroupSession(t.Context(), nested, engine.GroupSessionArgs{Action: "start", Title: "Nested", Message: "must not create"}); err == nil {
		t.Fatal("child acquired parent delegation authority")
	}
	if _, err := r.service.ToolSessionNew(t.Context(), caller, engine.SessionNewArgs{Title: "No private handler", Message: "must not create"}); err == nil {
		t.Fatal("group delegation enabled the private session handler")
	}
	// Revocation applies to queued child input before a restored/native admission can dispatch.
	groupSettings(t, r, "bot", "123:synthetic", "43")
	if _, err := r.q.CreateParentTurn(t.Context(), sqlc.CreateParentTurnParams{ID: "revoked-child-turn", ConversationID: id, Content: "must not run", WhenBusy: "followUp", ParentRequestID: sql.NullString{String: "group-parent-run", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, "revoked-child-turn")); err != nil {
		t.Fatal(err)
	}
	revoked := receipt(t, r, "revoked-child-turn")
	if revoked.Status != "unanswered" || revoked.ChannelRevoked == 0 || revoked.AdmittedAt.Valid {
		t.Fatalf("child retained a revoked actor grant: %+v", revoked)
	}
}

func TestUserGroupDelegationUsesNativeToolAndAudienceOnlyRead(t *testing.T) {
	// Given a real group registry and a public child operation.
	r := startNative(t, "spawn")
	groupSettings(t, r, "bot", "123:synthetic", "42")
	identity := ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7, MessageID: "1"}
	body := `{"tool":"group_session","action":"start","title":"Public child","message":"{\"tool\":\"group_note\",\"text\":\"public child note\"}"}`
	parent, source := groupInput(t, r, "bot", "native-group-parent", identity, body)
	children, err := r.q.ListAudienceChildren(t.Context(), sqlc.ListAudienceChildrenParams{AgentID: "bot", ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}, ExecutionScope: parent.ExecutionScope, ScopeContext: parent.ScopeContext})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("native group_session did not create exactly one audience child: %+v; parent=%+v", children, source)
	}
	child := children[0]
	var childTurn string
	if err := r.conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE conversation_id=? AND parent_request_id=?", child.ID, source.ID).Scan(&childTurn); err != nil {
		t.Fatal(err)
	}
	// When the durable child runs, its actual tool writes only the audience note.
	if err := r.service.admit(t.Context(), taskInput(t, r, childTurn)); err != nil {
		t.Fatal(err)
	}
	got := awaitReceipt(t, r, childTurn)
	if got.Status != "done" {
		t.Fatalf("child failed: %+v", got)
	}
	note, err := os.ReadFile(filepath.Join(r.dir, "agents", "bot", "telegram-groups", parent.ExecutionScope, "resources", strings.TrimPrefix(got.Answer.String, "spawn: ")))
	if err != nil || !strings.Contains(string(note), "public child note") {
		t.Fatalf("child did not retain the actual resource root: %q %v", note, err)
	}
	var result string
	if err := r.conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE conversation_id=? AND result_child_id=? AND kind='task_result'", parent.ID, child.ID).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, result)); err != nil {
		t.Fatal(err)
	}
	report := awaitReceipt(t, r, result)
	if report.Status != "done" || !strings.HasPrefix(report.Answer.String, "task-result:") {
		t.Fatalf("parent result did not retain the group source: %+v", report)
	}
	// A reopened engine must resume this child's existing native conversation in the same root.
	waitFor(t, func() bool { _, waiting := r.service.waiting.Load(result); return !waiting })
	if err := r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), FakeModel: true, Tasks: engine.TaskTools{AuthorizeGroup: r.service.AuthorizeGroup, GroupSession: r.service.ToolGroupSession}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()
	r.service.engine = reopened
	otherIdentity := identity
	otherIdentity.TopicID = 8
	other, _ := groupInput(t, r, "bot", "other-topic", otherIdentity, `{"tool":"group_note","text":"other topic note"}`)
	// Then new native tool calls can list and read only this audience's direct child.
	invoke := func(source, action, id, message string) sqlc.Submission {
		t.Helper()
		body, err := json.Marshal(map[string]string{"tool": "group_session", "action": action, "id": id, "message": message})
		if err != nil {
			t.Fatal(err)
		}
		_, got := groupInput(t, r, "bot", source, identity, string(body))
		if got.Status != "done" {
			t.Fatalf("native group action %s failed before its tool result: %+v", action, got)
		}
		return got
	}
	childID := strconv.FormatInt(child.ID, 10)
	read := invoke("native-child-read", "read", childID, "")
	if !strings.HasPrefix(read.Answer.String, "spawn: ") || !strings.Contains(read.Answer.String, "public child note") {
		t.Fatalf("native group read=%+v", read)
	}
	for _, action := range []string{"read", "send", "stop"} {
		denied := invoke("native-other-topic-"+action, action, strconv.FormatInt(other.ID, 10), "must not cross")
		if !strings.HasPrefix(denied.Answer.String, "spawn error: ") || !strings.Contains(denied.Answer.String, ErrSessionNotFound.Error()) {
			t.Fatalf("native group %s crossed topic ownership: %+v", action, denied)
		}
	}
	// A parent link alone cannot grant access to a foreign or private execution store.
	var foreignConversations []int64
	for i, scope := range []string{"", other.ExecutionScope} {
		foreign, err := r.q.CreateChildConversation(t.Context(), sqlc.CreateChildConversationParams{AgentID: "bot", ChannelKey: "foreign-child-" + strconv.Itoa(i), Title: "PRIVATE_CHILD_CANARY", ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}, ExecutionScope: scope, ScopeContext: other.ScopeContext})
		if err != nil {
			t.Fatal(err)
		}
		foreignConversations = append(foreignConversations, foreign.ID)
		for _, action := range []string{"read", "send", "stop"} {
			denied := invoke("native-foreign-"+strconv.Itoa(i)+"-"+action, action, strconv.FormatInt(foreign.ID, 10), "must not cross")
			if !strings.HasPrefix(denied.Answer.String, "spawn error: ") || !strings.Contains(denied.Answer.String, ErrSessionNotFound.Error()) {
				t.Fatalf("native group %s accepted a foreign store: %+v", action, denied)
			}
		}
	}
	listed := invoke("native-child-list", "list", "", "")
	var list engine.GroupSessionResult
	if err := json.Unmarshal([]byte(strings.TrimPrefix(listed.Answer.String, "spawn: ")), &list); err != nil || len(list.Sessions) != 1 || list.Sessions[0].ID != childID {
		t.Fatalf("private or foreign child entered native group list: %+v %v", listed, err)
	}
	sent := invoke("native-restored-child-send", "send", childID, `{"tool":"group_note","text":"restored public child note"}`)
	if !strings.HasPrefix(sent.Answer.String, "spawn: ") {
		t.Fatalf("native group send=%+v", sent)
	}
	var restoredTurn string
	if err := r.conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE conversation_id=? AND parent_request_id=?", child.ID, sent.ID).Scan(&restoredTurn); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, restoredTurn)); err != nil {
		t.Fatal(err)
	}
	restored := awaitReceipt(t, r, restoredTurn)
	if restored.Status != "done" {
		t.Fatalf("restored child failed: %+v", restored)
	}
	note, err = os.ReadFile(filepath.Join(r.dir, "agents", "bot", "telegram-groups", parent.ExecutionScope, "resources", strings.TrimPrefix(restored.Answer.String, "spawn: ")))
	if err != nil || string(note) != "restored public child note" {
		t.Fatalf("restored child changed resource root: %q %v", note, err)
	}
	var foreignTurns []string
	for i, conversationID := range foreignConversations {
		foreignTurn := "foreign-pending-" + strconv.Itoa(i)
		if _, err := r.q.CreateParentTurn(t.Context(), sqlc.CreateParentTurnParams{ID: foreignTurn, ConversationID: conversationID, Content: "private pending canary", WhenBusy: "followUp", ParentRequestID: sql.NullString{String: source.ID, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		foreignTurns = append(foreignTurns, foreignTurn)
	}
	if _, err := r.q.CreateParentTurn(t.Context(), sqlc.CreateParentTurnParams{ID: "group-pending-at-stop", ConversationID: child.ID, Content: "public pending work", WhenBusy: "followUp", ParentRequestID: sql.NullString{String: source.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.Stop(t.Context(), "bot", parent.ID); err != nil {
		t.Fatal(err)
	}
	if got := receipt(t, r, "group-pending-at-stop"); got.Status != "unanswered" || got.Reason.String != "stopped" {
		t.Fatalf("group Stop did not stop its same-audience child: %+v", got)
	}
	for _, id := range foreignTurns {
		if got := receipt(t, r, id); got.Status != "pending" {
			t.Fatalf("group parent Stop crossed execution-scope ownership: %+v", got)
		}
	}
}
