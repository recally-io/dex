package feishu_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCannotFinishFeishuRegistrationAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given a pending registration and a local accounts endpoint that approves it.
	path := filepath.Join(t.TempDir(), "control.sqlite")
	_, q, id := setupDB(t, path)
	writer, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	writer.SetMaxOpenConns(1)
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("action") == "begin" {
			_, _ = io.WriteString(w, `{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`)
			return
		}
		_, _ = io.WriteString(w, `{"client_id":"qr-app","client_secret":"test-secret","user_info":{"open_id":"creator","tenant_brand":"feishu"}}`)
	}))
	t.Cleanup(accounts.Close)
	var started bool
	s := feishu.NewSetup(q, writer, new(sync.Mutex), "", accounts.URL, func(_ string, cfg *feishu.Config) { started = started || cfg != nil }, nil)
	setup, err := s.Begin(t.Context(), id, "feishu")
	if err != nil {
		t.Fatal(err)
	}

	// When the archive commits while the approved registration waits for its writer.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := db.ArchiveBot(ctx, sqlc.New(tx), id, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The only writer connection is reserved. WAL readers still see the active bot.
	waitCount := writer.Stats().WaitCount
	type result struct {
		status string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		tick := time.Tick(20 * time.Millisecond)
		for {
			res, err := s.Poll(ctx, id, setup.Session)
			if err != nil || res.Status != "pending" {
				done <- result{res.Status, err}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-tick:
			}
		}
	}()
	tick := time.Tick(time.Millisecond)
	for writer.Stats().WaitCount == waitCount {
		select {
		case got := <-done:
			t.Fatalf("request returned before reaching the writer: %+v", got)
		case <-ctx.Done():
			t.Fatal("request did not reach the writer")
		case <-tick:
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Then the request fails without storing credentials or asking the runtime to start.
	select {
	case got := <-done:
		cfg, err := s.Config(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(got.err, db.ErrBotArchived) || got.status == "created" || cfg != nil || started {
			t.Fatalf("after archive: status=%q err=%v credentials saved=%t start requested=%t", got.status, got.err, cfg != nil, started)
		}
	case <-ctx.Done():
		t.Fatal("request did not finish after the archive")
	}
}

func TestUserCannotDisconnectFeishuAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given saved credentials, archive the bot as the old runtime stops.
	conn, q, id := setupDB(t, filepath.Join(t.TempDir(), "control.sqlite"))
	ctx := t.Context()
	if err := q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{BotID: id, AppID: "saved-app", AppSecret: "test-secret", Brand: "feishu"}); err != nil {
		t.Fatal(err)
	}
	var archiveErr error
	s := feishu.NewSetup(q, conn, new(sync.Mutex), "", "", func(_ string, cfg *feishu.Config) {
		if cfg == nil {
			archiveErr = db.ArchiveBot(ctx, q, id, time.Now())
		}
	}, nil)

	// When disconnect continues after the archive commits, then the archived bot keeps its credentials.
	disconnectErr := s.Delete(ctx, id)
	if archiveErr != nil {
		t.Fatal(archiveErr)
	}
	cfg, err := s.Config(ctx, id)
	if !errors.Is(disconnectErr, db.ErrBotArchived) || err != nil || cfg == nil || cfg.AppID != "saved-app" {
		t.Fatalf("after archive: disconnect err=%v saved credentials=%t read err=%v", disconnectErr, cfg != nil, err)
	}
}

func TestUserCannotPairFeishuAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given an archived bot with a still-valid Feishu code and a runtime that has not stopped yet.
	conn, q, id := setupDB(t, filepath.Join(t.TempDir(), "control.sqlite"))
	ctx := t.Context()
	if err := q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{BotID: id, AppID: "app", AppSecret: "secret", Brand: "feishu", PairingCode: "code", PairingExpiresAt: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := db.ArchiveBot(ctx, q, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := feishu.NewSetup(q, conn, new(sync.Mutex), "", "", nil, nil)

	// When the runtime delivers the code, then the archived bot must not gain an owner.
	paired, pairErr := s.Pair(ctx, id, "owner", "code")
	row, err := q.GetFeishuSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if paired || !errors.Is(pairErr, db.ErrBotArchived) || row.OwnerID != "" {
		t.Fatalf("after archive: Feishu paired=%t owner=%q err=%v", paired, row.OwnerID, pairErr)
	}
}

func setupDB(t *testing.T, path string) (*sql.DB, *sqlc.Queries, string) {
	t.Helper()
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	const id = "coder"
	if err := q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: id, Name: "Coder"}); err != nil {
		t.Fatal(err)
	}
	return conn, q, id
}
