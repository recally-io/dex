package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

func TestUserRevokedAfterCommandPreflightCannotStopOrClear(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	const bot = "bot"
	if err := q.CreateBotIfMissing(ctx, sqlc.CreateBotIfMissingParams{ID: bot, Name: "Bot"}); err != nil {
		t.Fatal(err)
	}
	setOwner := func(allowed string) {
		t.Helper()
		if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: bot, Token: "123:fixture", AllowedUsers: allowed}); err != nil {
			t.Fatal(err)
		}
	}
	setOwner("42")
	bots, err := engine.New(engine.Config{DataDir: dir, Home: t.TempDir(), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bots.Close(context.WithoutCancel(ctx)) })
	s := session.New(conn, bots, time.Now, nil, nil)
	bindSessionChannels(s)
	manager := newBotChannels(ctx, conn, s, bot)
	t.Cleanup(manager.Close)
	target, err := s.Main(ctx, bot)
	if err != nil {
		t.Fatal(err)
	}

	// Given persisted commands whose sender passed preflight while paired.
	const peer, stopSource, clearSource = "telegram:42", "telegram:123:1", "telegram:123:2"
	actor := session.ChannelContext{AccountID: "123", SenderID: "42"}
	for source, command := range map[string]string{stopSource: "/stop", clearSource: "/clear"} {
		if err := q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{BotID: bot, SourceKey: source, Peer: peer, ChannelKey: target.ChannelKey, Command: command, ChannelContext: `{"accountId":"123","senderId":"42"}`}); err != nil {
			t.Fatal(err)
		}
		if err := manager.RequirePairedChannel(ctx, q, peer, actor); err != nil {
			t.Fatal(err)
		}
	}
	// When the grant is revoked and restored before the stale commands reach the writer.
	setOwner("43")
	setOwner("42")
	if err := manager.RequirePairedChannel(ctx, q, peer, actor); err != nil {
		t.Fatal(err)
	}

	// Then re-pairing does not authorize either canceled command to change Main.
	if err := s.StopChannelCommand(ctx, bot, target.ID, stopSource, "stop requested"); !errors.Is(err, session.ErrChannelUnauthorized) {
		t.Errorf("canceled Stop reached its effect: %v", err)
	}
	if err := s.Clear(ctx, bot, target.ID, "channel:"+clearSource, peer, ""); !errors.Is(err, session.ErrChannelUnauthorized) {
		t.Errorf("canceled Clear reached its effect: %v", err)
	}
	stored, err := q.GetChannelCommand(ctx, stopSource)
	if err != nil || stored.Answer != "" {
		t.Errorf("canceled Stop wrote its answer: %q, %v", stored.Answer, err)
	}
	if _, err := q.GetSubmission(ctx, "channel:"+clearSource); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("canceled Clear wrote a reset: %v", err)
	}
}
