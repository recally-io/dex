package delivery_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/channel/delivery"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

func TestUserCannotReachMainThroughNonPrivateTelegramPeer(t *testing.T) {
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
	service := session.New(conn, agents, time.Now, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	defer func() { cancel(); <-done }()
	manager := delivery.New(ctx, conn, service, "bot")
	defer manager.Close()
	// When a group or malformed peer sends a command with valid-looking actor IDs.
	for _, peer := range []string{"telegram:-100", "telegram:042", "telegram:0"} {
		err := manager.Receive(ctx, peer, "group-command", session.Message{Content: "/clear", ChannelContext: session.ChannelContext{AccountID: "123", SenderID: "42"}})
		if err == nil {
			t.Fatalf("non-private peer %q reached Main", peer)
		}
	}
	// Then no command receipt or private conversation can be created by that request.
	var commands, sessions int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM channel_commands").Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if commands != 0 || sessions != 0 {
		t.Fatalf("private command side effects preceded rejection: commands=%d sessions=%d", commands, sessions)
	}
}
