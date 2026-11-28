package bot_test

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

	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCannotReplaceWechatAccountAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given a connected account and a QR login that would replace it.
	q, writer, id := archiveRaceDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = io.WriteString(w, `{"qrcode":"code","qrcode_img_content":"QR"}`)
		case "/ilink/bot/get_qrcode_status":
			_, _ = io.WriteString(w, `{"status":"confirmed","bot_token":"new-token","ilink_bot_id":"new-bot","ilink_user_id":"new-owner","baseurl":"https://example.com"}`)
		default:
			t.Errorf("unexpected WeChat request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	if err := q.SetWechatSettings(t.Context(), sqlc.SetWechatSettingsParams{BotID: id, Token: "old-token", BaseUrl: server.URL, WechatBotID: "old-bot", Owner: "old-owner"}); err != nil {
		t.Fatal(err)
	}
	s := bot.New(q, writer, new(sync.Mutex), nil, "", nil)
	var started bool
	s.ConfigureWechat(wechat.Options{APIURL: server.URL}, func(_ string, cfg *wechat.Config) { started = started || cfg != nil })
	login, err := s.BeginWechatLogin(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	// When the archive commits while the login waits for its writer.
	var status string
	saveErr := archiveBeforeChannelWrite(t, writer, id, func(ctx context.Context) error {
		view, err := s.PollWechatLogin(ctx, id, login.ID, "")
		status = view.Status
		return err
	})

	// Then the previous credentials remain, and neither account starts.
	cfg, err := s.WechatConfig(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, runtimeErr := q.GetWechatRuntime(t.Context(), sqlc.GetWechatRuntimeParams{BotID: "new-bot", Owner: "new-owner"})
	account := ""
	if cfg != nil {
		account = cfg.BotID
	}
	if !errors.Is(saveErr, db.ErrBotArchived) || status == "confirmed" || account != "old-bot" || started || !errors.Is(runtimeErr, sql.ErrNoRows) {
		t.Fatalf("after archive: status=%q err=%v account=%q start requested=%t replacement runtime err=%v", status, saveErr, account, started, runtimeErr)
	}
}

func TestUserCannotSaveTelegramSettingsAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given saved Telegram credentials and a request to replace them.
	q, writer, id := archiveRaceDB(t)
	if _, err := q.SetTelegramSettings(t.Context(), sqlc.SetTelegramSettingsParams{BotID: id, Token: "123:old", Options: "{}"}); err != nil {
		t.Fatal(err)
	}
	var started bool
	var archiveErr error
	s := bot.New(sqlc.New(writer), writer, new(sync.Mutex), nil, "", func(_ string, cfg *telegram.Config) {
		if cfg != nil {
			started = true
			return
		}
		// Commit the archive as the old account stops, before the settings write.
		archiveErr = db.ArchiveBot(t.Context(), q, id, time.Now())
	})

	// When the save continues after the archive commits.
	saveErr := s.SetTelegram(t.Context(), id, "456:new", nil, nil, "", nil)
	if archiveErr != nil {
		t.Fatal(archiveErr)
	}

	// Then the old credentials remain and no runtime starts.
	cfg, err := s.TelegramConfig(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	kept := cfg != nil && cfg.Token == "123:old"
	if !errors.Is(saveErr, db.ErrBotArchived) || !kept || started {
		t.Fatalf("after archive: err=%v old credentials kept=%t start requested=%t", saveErr, kept, started)
	}
}

func TestUserCannotPairTelegramAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given an archived bot with still-valid a pairing code and a runtime that has not stopped yet.
	q, writer, id := archiveRaceDB(t)
	ctx := t.Context()
	if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: id, Token: "123:secret", Options: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetTelegramPairing(ctx, sqlc.SetTelegramPairingParams{BotID: id, PairingCode: "code", PairingExpires: time.Now().Add(time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	s := bot.New(q, writer, new(sync.Mutex), nil, "", nil)
	tg, err := s.TelegramConfig(ctx, id)
	if err != nil || tg == nil {
		t.Fatalf("read initial Telegram settings: configured=%t err=%v", tg != nil, err)
	}
	if err := db.ArchiveBot(ctx, q, id, time.Now()); err != nil {
		t.Fatal(err)
	}

	// When the runtime delivers a pairing request, the archived bot must not gain an owner.
	users, telegramErr := tg.Pair(ctx, "code", 42)
	telegramRow, err := q.GetTelegramSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 || !errors.Is(telegramErr, db.ErrBotArchived) || telegramRow.AllowedUsers != "" {
		t.Fatalf("after archive: Telegram users=%v saved=%q err=%v", users, telegramRow.AllowedUsers, telegramErr)
	}
}

func archiveRaceDB(t *testing.T) (*sqlc.Queries, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	open := func() *sql.DB {
		conn, err := db.Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	q := sqlc.New(open())
	const id = "coder"
	if err := q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: id, Name: "Coder"}); err != nil {
		t.Fatal(err)
	}
	writer := open()
	writer.SetMaxOpenConns(1)
	return q, writer, id
}

func archiveBeforeChannelWrite(t *testing.T, writer *sql.DB, id string, save func(context.Context) error) error {
	t.Helper()
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
	// The only connection in this pool is reserved. WAL readers can still see the active bot.
	waitCount := writer.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- save(ctx) }()
	tick := time.Tick(time.Millisecond)
	for writer.Stats().WaitCount == waitCount {
		select {
		case err := <-done:
			t.Fatalf("request returned before reaching the writer: %v", err)
		case <-ctx.Done():
			t.Fatal("request did not reach the writer")
		case <-tick:
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("request did not finish after the archive")
		return nil
	}
}
