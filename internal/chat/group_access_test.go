package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserTelegramGroupPeerCannotBorrowMain(t *testing.T) {
	// Given a private bot with no trusted group identity on the input.
	r := startNative(t, "echo")
	// When a channel caller supplies only a group peer and an allowed-looking sender.
	err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100", "unbound-group", Message{Content: "private route", ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"}})
	// Then the caller cannot route that input through the private Main runtime.
	if err == nil {
		t.Fatal("group input without a bound audience entered private Main")
	}
	var count int
	if err := r.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key = 'unbound-group'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unbound source persisted: count=%d err=%v", count, err)
	}
}

func groupSettings(t *testing.T, r *nativeTest, agentID, token, users string) {
	t.Helper()
	if _, err := r.q.SetTelegramSettings(t.Context(), sqlc.SetTelegramSettingsParams{AgentID: agentID, Token: token, AllowedUsers: users}); err != nil {
		t.Fatal(err)
	}
}

func groupInput(t *testing.T, r *nativeTest, agentID, source string, identity ChannelContext, text string) (sqlc.Conversation, sqlc.Submission) {
	t.Helper()
	peer := fmt.Sprintf("telegram:%d", identity.ChatID)
	if identity.TopicID > 0 {
		peer += fmt.Sprintf(":%d", identity.TopicID)
	}
	if err := r.service.SubmitMessage(t.Context(), agentID, peer, source, Message{Content: text, ChannelContext: identity}); err != nil {
		t.Fatal(err)
	}
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: source, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	receipt := awaitReceipt(t, r, id)
	conv, err := r.q.GetConversation(t.Context(), receipt.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	return conv, receipt
}

func TestUserTelegramGroupScopeDoesNotGrantOwnerOrActorAccess(t *testing.T) {
	// Given a private Main and a group audience whose actor is independently paired.
	r := startNative(t, "echo")
	privateID, err := r.service.Post(t.Context(), "bot", MainKey, "", "private-input", "PRIVATE_HISTORY_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "private-input")
	private, err := r.q.GetConversation(t.Context(), privateID)
	if err != nil {
		t.Fatal(err)
	}
	groupSettings(t, r, "bot", "123:synthetic", "42")
	identity := ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7, MessageID: "1"}
	group, receipt := groupInput(t, r, "bot", "group-source", identity, "PUBLIC_GROUP_CANARY")
	if receipt.Answer.String != "echo: PUBLIC_GROUP_CANARY" || group.ExecutionScope != "tg_123_-100_7" || group.ExecutionID.Int64 != private.ExecutionID.Int64 {
		t.Fatalf("test needs equal IDs in separate stores: private=%+v group=%+v receipt=%+v", private, group, receipt)
	}
	// When a caller supplies that native ID but borrows the other scope or an unpaired actor.
	caller := engine.TaskContext{AgentID: "bot", Scope: group.ExecutionScope, ConversationID: group.ExecutionID.Int64, SourceRequestID: receipt.ID, Key: "group-call"}
	if err := r.service.ToolBotCaller(t.Context(), caller, false); err == nil {
		t.Fatal("group actor read private bot settings")
	}
	if _, err := r.service.ToolSessionRead(t.Context(), caller, engine.SessionReadArgs{}); err == nil {
		t.Fatal("group actor entered private session history")
	}
	caller.Scope = ""
	if err := r.service.ToolBotCaller(t.Context(), caller, false); err == nil {
		t.Fatal("group source borrowed private owner authority through an equal native ID")
	}
	identity.TopicID = 8
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:8", "group-source", Message{Content: "reused source", ChannelContext: identity}); err == nil {
		t.Fatal("source deduplication crossed topic ownership")
	}
	identity.TopicID = 7
	identity.SenderID = "43"
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:7", "unpaired", Message{Content: "unpaired", ChannelContext: identity}); err == nil {
		t.Fatal("a valid scope granted access to an unpaired sender")
	}
	identity.SenderID = "42"
	identity.AccountID = "456"
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:7", "wrong-account", Message{Content: "wrong bot", ChannelContext: identity}); err == nil {
		t.Fatal("a saved audience survived a different Telegram account")
	}
	// Then private and group history resolve from their persisted store, never by the native ID alone.
	for _, test := range []struct {
		conv sqlc.Conversation
		text string
	}{{private, "PRIVATE_HISTORY_CANARY"}, {group, "PUBLIC_GROUP_CANARY"}} {
		full, err := r.service.Get(t.Context(), "bot", test.conv.ID)
		if err != nil || full.ExecutionScope != test.conv.ExecutionScope {
			t.Fatalf("business scope lost: %+v err=%v", full, err)
		}
		ref := engine.Ref{AgentID: "bot", ID: full.ExecutionID, Scope: full.ExecutionScope}
		history, err := r.agents.History(t.Context(), ref, 1<<62, 100)
		if err != nil {
			t.Fatal(err)
		}
		var users []string
		for _, entry := range history.Items {
			if entry.Kind == "user" {
				users = append(users, entry.Text)
			}
		}
		if len(users) != 1 || users[0] != test.text {
			t.Fatalf("history crossed scope: %v", users)
		}
	}
	if userOrigin("telegram:-100:7") || userOrigin("telegram:-100") {
		t.Fatal("group input acquired private owner-origin authority")
	}
}

func TestUserTelegramGroupSessionsFollowSenderAndTopicAudience(t *testing.T) {
	// Given two paired actors in an ordinary group and in a forum topic.
	r := startNative(t, "echo")
	groupSettings(t, r, "bot", "123:synthetic", "42,43")
	var sessions []sqlc.Conversation
	for i, identity := range []ChannelContext{
		{AccountID: "123", SenderID: "42", ChatID: -100},
		{AccountID: "123", SenderID: "43", ChatID: -100},
		{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7},
		{AccountID: "123", SenderID: "43", ChatID: -100, TopicID: 7},
		{AccountID: "123", SenderID: "42", ChatID: -200, TopicID: 7},
	} {
		// When each actor sends an independently owned platform input.
		conv, got := groupInput(t, r, "bot", fmt.Sprintf("session-source-%d", i), identity, fmt.Sprintf("public-%d", i))
		if got.Status != "done" {
			t.Fatalf("group input failed: %+v", got)
		}
		sessions = append(sessions, conv)
	}
	if err := r.q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "other", Name: "Other", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	groupSettings(t, r, "other", "456:synthetic", "42")
	other, got := groupInput(t, r, "other", "other-bot-source", ChannelContext{AccountID: "456", SenderID: "42", ChatID: -100, TopicID: 7}, "other bot public context")
	if got.Status != "done" || other.ExecutionScope == sessions[2].ExecutionScope || other.ExecutionID != sessions[2].ExecutionID {
		t.Fatalf("bot namespace lost: other=%+v same-chat=%+v", other, sessions[2])
	}
	// Then ordinary groups separate sender sessions but share an audience; topics share only their own session.
	if sessions[0].ID == sessions[1].ID || sessions[0].ExecutionScope != sessions[1].ExecutionScope || sessions[2].ID != sessions[3].ID || sessions[2].ExecutionScope == sessions[4].ExecutionScope || sessions[0].ExecutionScope == sessions[2].ExecutionScope {
		t.Fatalf("wrong sender/topic routing: %+v", sessions)
	}
}

func TestUserTelegramGroupToolRechecksActorAfterProviderRequest(t *testing.T) {
	// Given a real native run whose model response is held at the provider boundary.
	r := startNative(t, "echo")
	groupSettings(t, r, "bot", "123:synthetic", "42")
	identity := ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7, MessageID: "1"}
	conv, _ := groupInput(t, r, "bot", "before-provider-gate", identity, "public context")
	entered := make(chan struct{}, 1)
	gate, release := context.WithCancel(t.Context())
	defer release()
	provider := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "actor-gate", Models: []ai.FauxModelDefinition{{ID: "gate"}}, TokensPerSecond: 10000})
	provider.SetResponses([]ai.FauxResponseStep{ai.FauxFactoryStep(func(_ ai.TranscriptContext, options ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
		entered <- struct{}{}
		select {
		case <-gate.Done():
		case <-options.Signal.Done():
			return ai.AssistantMessage{}, options.Signal.Err()
		}
		return ai.FauxAssistantMessage(ai.FauxContentBlocks{ai.FauxToolCall("group_note", map[string]any{"text": "must not be written after revocation"}, nil)}, ai.FauxAssistantMessageOptions{StopReason: "toolUse"}), nil
	})})
	r.agents.Catalog.Models.SetProvider(provider.Provider())
	if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "actor-gate", ModelID: "gate", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:7", "revoked-between-request-and-tool", Message{Content: "write a note", ChannelContext: identity}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// When the actor is revoked after the provider request passed authorization but before dispatch.
	groupSettings(t, r, "bot", "123:synthetic", "43")
	release()
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "revoked-between-request-and-tool", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	outcome := awaitReceipt(t, r, id)
	// Then the actual tool cannot write, and another provider request cannot follow the denial.
	files, err := os.ReadDir(filepath.Join(r.dir, "agents", "bot", "telegram-groups", conv.ExecutionScope, "resources"))
	if err != nil || len(files) != 0 || outcome.Status != "unanswered" || outcome.Reason.String != "aborted" {
		t.Fatalf("revocation lost at execution: files=%v outcome=%+v err=%v", files, outcome, err)
	}
}

func TestUserTelegramGroupToolsCheckCurrentActorAfterNativeRestore(t *testing.T) {
	// Given an admitted source whose actor was paired, and native storage that can reopen independently.
	r := startNative(t, "direct")
	groupSettings(t, r, "bot", "123:synthetic", "42")
	identity := ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7, MessageID: "1"}
	conv, got := groupInput(t, r, "bot", "initial-note", identity, `group_note {"text":"first public note"}`)
	if got.Status != "done" || !strings.Contains(got.Answer.String, "note-") {
		t.Fatalf("initial note: %+v", got)
	}
	root := filepath.Join(r.dir, "agents", "bot", "telegram-groups", conv.ExecutionScope, "resources")
	initial, err := os.ReadDir(root)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial notes=%v err=%v", initial, err)
	}
	waitFor(t, func() bool { _, waiting := r.service.waiting.Load(got.ID); return !waiting })
	if err := r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), FakeModel: true, Tasks: engine.TaskTools{AuthorizeGroup: r.service.AuthorizeGroup}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()
	r.service.engine = reopened
	ref := engine.Ref{AgentID: "bot", ID: conv.ExecutionID.Int64, Scope: conv.ExecutionScope}
	run := func(id, text string) engine.Outcome {
		t.Helper()
		encoded, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: id, ConversationID: conv.ID, Content: text, Attachments: "null", Origin: "telegram:-100:7", Kind: "message", ChannelContext: string(encoded)}); err != nil {
			t.Fatal(err)
		}
		submission, err := reopened.Submit(t.Context(), ref, engine.Input{RequestID: id, Content: text, Kind: "message", Origin: "telegram:-100:7", Scope: ref.Scope, Model: engine.ModelRef{Provider: "faux", ModelID: "direct"}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		outcome, err := submission.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return outcome
	}
	// When a restored native operation uses the still-current actor, it retains the group resource path.
	allowed := run("restored-allowed", `group_note {"text":"restored public note"}`)
	if allowed.Status != "done" || !strings.Contains(allowed.Answer, "note-") {
		t.Fatalf("authorized restore failed: %+v", allowed)
	}
	before, err := os.ReadDir(root)
	if err != nil || len(before) != 2 {
		t.Fatalf("restored note count=%d err=%v", len(before), err)
	}
	// When the actor is revoked after the input identity was established, that same scope cannot authorize a new tool effect.
	groupSettings(t, r, "bot", "123:synthetic", "43")
	denied := run("restored-revoked", `group_note {"text":"must never be written"}`)
	after, err := os.ReadDir(root)
	if err != nil || len(after) != len(before) || denied.Status != "unanswered" || denied.Reason != "aborted" || denied.Answer != "" {
		t.Fatalf("revoked native tool executed: before=%d after=%d outcome=%+v err=%v", len(before), len(after), denied, err)
	}
	allowedReply, err := r.service.GroupReplyAllowed(t.Context(), "bot", "restored-allowed")
	if err != nil || allowedReply {
		t.Fatalf("revoked actor retained public delivery: allowed=%v err=%v", allowedReply, err)
	}
	// Then fresh business admission also refuses the revoked source without creating a receipt.
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:7", "after-revocation", Message{Content: "no", ChannelContext: identity}); err == nil {
		t.Fatal("revoked actor entered a new native run")
	}
}
