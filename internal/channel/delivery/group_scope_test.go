package delivery_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/channel/delivery"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserGroupIdentityCannotAcquireMainThroughCommandPeer(t *testing.T) {
	// Given real business services and native execution, without an exposed owner-control route.
	directory := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(directory, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "bot", Name: "Bot"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	agents, err := engine.New(engine.Config{DataDir: directory, Home: home, BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer agents.Close(context.WithoutCancel(t.Context()))
	service := chat.New(conn, agents, time.Now, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	defer func() { cancel(); <-done }()
	manager := delivery.New(ctx, conn, service, "bot")
	defer manager.Close()
	// When group identity is mislabeled as a positive private command peer, reject before Main is touched.
	err = manager.Receive(ctx, "telegram:42", "mismatched-group-command", chat.Message{Content: "/clear", ChannelContext: chat.ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7}})
	if err == nil {
		t.Fatal("group metadata acquired Main through a private command peer")
	}
	// Then no command receipt or private conversation can be created by that request.
	var commands, conversations int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM channel_commands").Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM conversations").Scan(&conversations); err != nil {
		t.Fatal(err)
	}
	if commands != 0 || conversations != 0 {
		t.Fatalf("private command side effects preceded rejection: commands=%d conversations=%d", commands, conversations)
	}
}
