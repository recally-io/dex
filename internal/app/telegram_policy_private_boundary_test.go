package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

func TestUserCannotReachMainThroughNonPrivateTelegramPeer(t *testing.T) {
	t.Parallel()
	// Given real business services and native execution, without an exposed owner-control route.
	directory := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(directory, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	if err := q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: "bot", Name: "Bot"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateBotSettings(t.Context(), sqlc.UpdateBotSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	bots, err := engine.New(engine.Config{DataDir: directory, Home: home, BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer bots.Close(context.WithoutCancel(t.Context()))
	service := session.New(conn, bots, time.Now, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	defer func() { cancel(); <-done }()
	manager := newBotChannels(ctx, conn, service, "bot")
	defer manager.Close()
	// When a group or malformed peer sends a command with valid-looking actor IDs.
	for _, peer := range []string{"telegram:-100", "telegram:042", "telegram:0"} {
		err := manager.Receive(ctx, peer, "group-command", session.Message{Content: "/clear", ChannelContext: session.ChannelContext{AccountID: "123", SenderID: "42"}})
		if err == nil {
			t.Fatalf("non-private peer %q reached Main", peer)
		}
	}
	// An input with a private peer but a revoked actor must also leave Main absent.
	if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: "bot", Token: "123:synthetic", AllowedUsers: "43"}); err != nil {
		t.Fatal(err)
	}
	const source = "telegram:123:revoked"
	err = manager.Receive(ctx, "telegram:42", source, session.Message{Content: "revoked input", ChannelContext: session.ChannelContext{AccountID: "123", SenderID: "42"}})
	if !errors.Is(err, session.ErrTelegramUnauthorized) {
		t.Fatalf("revoked input did not get the authorization error: %v", err)
	}
	if received, err := service.ReceivedSource(ctx, source); err != nil || received {
		t.Fatalf("revoked input has a receipt: received=%t err=%v", received, err)
	}
	// Then no rejected input can create a command receipt or Main.
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
