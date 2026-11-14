package live_test

import (
	"bufio"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/live"
)

func TestUserGroupHistoryAndLiveReadTheirOwnNativeStore(t *testing.T) {
	// Given private and group conversations with equal native IDs in separate real stores.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	directory := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(directory, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{ID: "bot", Name: "Bot"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:synthetic", AllowedUsers: "42"}); err != nil {
		t.Fatal(err)
	}
	var service *chat.Service
	agents, err := engine.New(engine.Config{DataDir: directory, Home: t.TempDir(), FakeModel: true, Tasks: engine.TaskTools{AuthorizeGroup: func(ctx context.Context, caller engine.TaskContext) error { return service.AuthorizeGroup(ctx, caller) }}})
	if err != nil {
		t.Fatal(err)
	}
	defer agents.Close(context.WithoutCancel(ctx))
	service = chat.New(conn, agents, time.Now, nil, nil)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Run(runCtx) }()
	defer func() { stop(); <-done }()
	privateID, err := service.Post(ctx, "bot", chat.MainKey, "", "private", "PRIVATE_LIVE_HISTORY_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SubmitMessage(ctx, "bot", "telegram:-100:7", "group", chat.Message{Content: "PUBLIC_LIVE_HISTORY_CANARY", ChannelContext: chat.ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7, MessageID: "1"}}); err != nil {
		t.Fatal(err)
	}
	sourceID, err := q.GetSubmissionBySource(ctx, sql.NullString{String: "group", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	var source sqlc.Submission
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		source, err = q.GetSubmission(ctx, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		private, err := q.GetSubmission(ctx, "private")
		if err != nil {
			t.Fatal(err)
		}
		if source.Status != "pending" && private.Status != "pending" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	private, err := service.Get(ctx, "bot", privateID)
	if err != nil {
		t.Fatal(err)
	}
	group, err := service.Get(ctx, "bot", source.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if private.ExecutionID != group.ExecutionID || group.ExecutionScope == "" || group.Writable {
		t.Fatalf("scope precondition: private=%+v group=%+v", private, group)
	}
	hub := live.New(service, agents)
	// When history and SSE reconstruct a reference from the business chat, both must retain scope.
	history, err := hub.History(ctx, "bot", group.ID, 1<<62, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range history.Items {
		if strings.Contains(item.Text, "PRIVATE_LIVE_HISTORY_CANARY") {
			t.Fatalf("private history leaked through group route: %+v", item)
		}
		if strings.Contains(item.Text, "PUBLIC_LIVE_HISTORY_CANARY") {
			found = true
		}
	}
	if !found {
		t.Fatalf("group history missing: %+v", history)
	}
	stream, err := hub.Open(ctx, "bot", group.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reader := bufio.NewReader(stream)
	found = false
	for !found {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "PRIVATE_LIVE_HISTORY_CANARY") {
			t.Fatalf("private live snapshot leaked: %s", line)
		}
		found = strings.Contains(line, "PUBLIC_LIVE_HISTORY_CANARY")
	}
	// Then generic private input cannot reopen the group conversation in Main's resource scope.
	if _, err := service.Send(ctx, "bot", group.ChannelKey, chat.Message{ID: "private-route-injection", Content: "must not run"}); err == nil {
		t.Fatal("generic private input borrowed the group conversation")
	}
}
