package delivery_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/channel/delivery"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

func TestUserTelegramButtonClaimAndDurableIntentAreAtomic(t *testing.T) {
	// Given two live control entry points over real SQLite and the native service.
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
	if _, err := q.SetTelegramSettings(t.Context(), sqlc.SetTelegramSettingsParams{BotID: "bot", Token: "123:synthetic", AllowedUsers: "42"}); err != nil {
		t.Fatal(err)
	}
	bots, err := engine.New(engine.Config{DataDir: directory, Home: t.TempDir(), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer bots.Close(context.WithoutCancel(t.Context()))
	service := session.New(conn, bots, time.Now, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	defer func() { cancel(); <-done }()
	identity := session.ChannelContext{AccountID: "123", SenderID: "42", MessageID: "10"}
	target, err := service.Main(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string) {
		t.Helper()
		if err := q.CreateTelegramCallback(ctx, sqlc.CreateTelegramCallbackParams{ID: id, BotID: "bot", SourceID: id, Peer: "telegram:42", ChannelContext: string(encoded), SessionID: target.ID, ExecutionID: target.ExecutionID, ObjectID: strconv.FormatInt(target.ID, 10), Action: "/status", ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		if err := q.BindTelegramCallbackMessage(ctx, sqlc.BindTelegramCallbackMessageParams{SourceID: id, MessageID: "10"}); err != nil {
			t.Fatal(err)
		}
	}
	create("concurrent-button")
	managers := []*delivery.Manager{delivery.New(ctx, conn, service, "bot"), delivery.New(ctx, conn, service, "bot")}
	defer func() {
		for _, manager := range managers {
			manager.Close()
		}
	}()
	type result struct {
		accepted bool
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, manager := range managers {
		wg.Go(func() {
			ok, err := manager.ClaimTelegramCallback(ctx, "concurrent-button", "telegram:42", "10", identity)
			results <- result{ok, err}
		})
	}
	wg.Wait()
	accepted := 0
	for range managers {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.accepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("concurrent replay executed %d times", accepted)
	}
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM channel_commands WHERE source_key='telegram-callback:concurrent-button'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable intents=%d err=%v", count, err)
	}
	// When SQLite rejects intent storage, the consumed mark must roll back with it.
	create("rollback-button")
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_callback_intent BEFORE INSERT ON channel_commands WHEN NEW.source_key='telegram-callback:rollback-button' BEGIN SELECT RAISE(ABORT,'intent boundary failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := managers[0].ClaimTelegramCallback(ctx, "rollback-button", "telegram:42", "10", identity); err == nil || ok {
		t.Fatalf("failed durable intent claimed button: accepted=%t err=%v", ok, err)
	}
	stored, err := q.GetTelegramCallback(ctx, "rollback-button")
	if err != nil || stored.ConsumedAt.Valid {
		t.Fatalf("failed intent lost its button: %+v %v", stored, err)
	}
	// A durable intent restored after revocation cannot pass the actual effect writer.
	create("queued-revoked-stop")
	if _, err := conn.ExecContext(ctx, "UPDATE telegram_callbacks SET action='/stop' WHERE id='queued-revoked-stop'"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ConsumeTelegramCallback(ctx, sqlc.ConsumeTelegramCallbackParams{ID: "queued-revoked-stop", Now: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := session.RequirePairedChannel(ctx, q, "bot", "telegram:42", identity); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{BotID: "bot", SourceKey: "telegram-callback:queued-revoked-stop", Peer: "telegram:42", ChannelKey: target.ChannelKey, Command: "/stop", ChannelContext: string(encoded)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: "bot", Token: "123:synthetic", AllowedUsers: "43"}); err != nil {
		t.Fatal(err)
	}
	if err := service.StopChannelCommand(ctx, "bot", target.ID, "telegram-callback:queued-revoked-stop", "stop must not execute"); !errors.Is(err, session.ErrTelegramUnauthorized) {
		t.Fatalf("restored Stop bypassed current actor at its effect writer: %v", err)
	}
}
