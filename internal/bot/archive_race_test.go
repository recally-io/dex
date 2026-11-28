package bot_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCannotFinishFeishuRegistrationAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given a pending registration and a local accounts endpoint that approves it.
	q, writer, id := archiveRaceDB(t)
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("action") == "begin" {
			_, _ = io.WriteString(w, `{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`)
			return
		}
		_, _ = io.WriteString(w, `{"client_id":"qr-app","client_secret":"test-secret","user_info":{"open_id":"creator","tenant_brand":"feishu"}}`)
	}))
	t.Cleanup(accounts.Close)
	s := bot.New(q, writer, nil, "", nil)
	var started bool
	s.ConfigureFeishu("", accounts.URL, func(_ string, cfg *feishu.Config) { started = started || cfg != nil })
	setup, err := s.BeginFeishu(t.Context(), id, "feishu")
	if err != nil {
		t.Fatal(err)
	}

	// When the archive commits while the approved registration waits for its writer.
	var status string
	saveErr := archiveBeforeChannelWrite(t, writer, id, func(ctx context.Context) error {
		tick := time.Tick(20 * time.Millisecond)
		for {
			res, err := s.PollFeishu(ctx, id, setup.Session)
			status = res.Status
			if err != nil || status != "pending" {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-tick:
			}
		}
	})

	// Then the request fails without storing credentials or asking the runtime to start.
	cfg, err := s.FeishuConfig(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(saveErr, db.ErrBotArchived) || status == "created" || cfg != nil || started {
		t.Fatalf("after archive: status=%q err=%v credentials saved=%t start requested=%t", status, saveErr, cfg != nil, started)
	}
}

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
	s := bot.New(q, writer, nil, "", nil)
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
	s := bot.New(sqlc.New(writer), writer, nil, "", func(_ string, cfg *telegram.Config) {
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

func TestUserCannotPairChannelAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given an archived bot with still-valid pairing codes and a runtime that has not stopped yet.
	q, writer, id := archiveRaceDB(t)
	ctx := t.Context()
	if err := q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{BotID: id, AppID: "app", AppSecret: "secret", Brand: "feishu", PairingCode: "code", PairingExpiresAt: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: id, Token: "123:secret", Options: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetTelegramPairing(ctx, sqlc.SetTelegramPairingParams{BotID: id, PairingCode: "code", PairingExpires: time.Now().Add(time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	s := bot.New(q, writer, nil, "", nil)
	tg, err := s.TelegramConfig(ctx, id)
	if err != nil || tg == nil {
		t.Fatalf("read initial Telegram settings: configured=%t err=%v", tg != nil, err)
	}
	if err := db.ArchiveBot(ctx, q, id, time.Now()); err != nil {
		t.Fatal(err)
	}

	// When the runtime delivers pairing requests, the archived bot must not gain an owner.
	paired, feishuErr := s.PairFeishu(ctx, id, "owner", "code")
	users, telegramErr := tg.Pair(ctx, "code", 42)
	feishuRow, err := q.GetFeishuSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	telegramRow, err := q.GetTelegramSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if paired || !errors.Is(feishuErr, db.ErrBotArchived) || feishuRow.OwnerID != "" || len(users) != 0 || !errors.Is(telegramErr, db.ErrBotArchived) || telegramRow.AllowedUsers != "" {
		t.Fatalf("after archive: Feishu paired=%t owner=%q err=%v; Telegram users=%v saved=%q err=%v", paired, feishuRow.OwnerID, feishuErr, users, telegramRow.AllowedUsers, telegramErr)
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
