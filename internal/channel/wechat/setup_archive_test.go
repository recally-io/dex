package wechat_test

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

	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCannotReplaceWechatAccountAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given a connected account and a QR login that would replace it.
	q, writer, id := archiveRaceDB(t)
	server := confirmingWechat(t)
	if err := q.SetWechatSettings(t.Context(), sqlc.SetWechatSettingsParams{BotID: id, Token: "old-token", BaseUrl: server.URL, WechatBotID: "old-bot", Owner: "old-owner"}); err != nil {
		t.Fatal(err)
	}
	var started bool
	s := wechat.NewSetup(q, writer, new(sync.Mutex), wechat.Options{APIURL: server.URL}, func(_ string, cfg *wechat.Config) { started = started || cfg != nil }, nil)
	login, err := s.Begin(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	// When the archive commits while the login waits for its writer.
	var status string
	saveErr := commitBeforeChannelWrite(t, writer, func(ctx context.Context, q *sqlc.Queries) error {
		return db.ArchiveBot(ctx, q, id, time.Now())
	}, func(ctx context.Context) error {
		view, err := s.Poll(ctx, id, login.ID, "")
		status = view.Status
		return err
	})

	// Then the previous credentials remain, and neither account starts.
	cfg, err := s.Config(t.Context(), id)
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

func TestUserCannotDisconnectWechatAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given a connected account, archive the bot as the runtime stops.
	q, writer, id := archiveRaceDB(t)
	ctx := t.Context()
	if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{BotID: id, Token: "old-token", BaseUrl: "https://example.com", WechatBotID: "old-bot", Owner: "old-owner"}); err != nil {
		t.Fatal(err)
	}
	var archiveErr error
	var started bool
	s := wechat.NewSetup(q, writer, new(sync.Mutex), wechat.Options{}, func(_ string, cfg *wechat.Config) {
		if cfg == nil {
			archiveErr = db.ArchiveBot(ctx, q, id, time.Now())
		} else {
			started = true
		}
	}, nil)

	// When disconnect continues after the archive, then credentials stay saved and the runtime stays stopped.
	disconnectErr := s.Delete(ctx, id)
	if archiveErr != nil {
		t.Fatal(archiveErr)
	}
	cfg, err := s.Config(ctx, id)
	if !errors.Is(disconnectErr, db.ErrBotArchived) || err != nil || cfg == nil || cfg.BotID != "old-bot" || started {
		t.Fatalf("after archive: disconnect err=%v saved credentials=%t read err=%v start requested=%t", disconnectErr, cfg != nil, err, started)
	}
}

func TestUserCannotActivateWechatAccountClaimedDuringSave(t *testing.T) {
	t.Parallel()
	// Given a QR login that would link an unclaimed account.
	q, writer, id := archiveRaceDB(t)
	const other = "other"
	if err := q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: other, Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	server := confirmingWechat(t)
	var started bool
	s := wechat.NewSetup(q, writer, new(sync.Mutex), wechat.Options{APIURL: server.URL}, func(_ string, cfg *wechat.Config) { started = started || cfg != nil }, nil)
	login, err := s.Begin(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	// When another writer claims the account before activation gets the writer.
	claim := sqlc.SetWechatSettingsParams{BotID: other, Token: "new-token", BaseUrl: server.URL, WechatBotID: "new-bot", Owner: "new-owner"}
	saveErr := commitBeforeChannelWrite(t, writer, func(ctx context.Context, q *sqlc.Queries) error {
		return q.SetWechatSettings(ctx, claim)
	}, func(ctx context.Context) error {
		_, err := s.Poll(ctx, id, login.ID, "")
		return err
	})

	// Then activation returns a conflict and leaves the account with the other bot.
	cfg, err := s.Config(t.Context(), id)
	if !errors.Is(saveErr, wechat.ErrCredentialInUse) || err != nil || cfg != nil || started {
		t.Fatalf("after claim: err=%v read err=%v credentials saved=%t start requested=%t", saveErr, err, cfg != nil, started)
	}
}

func confirmingWechat(t *testing.T) *httptest.Server {
	t.Helper()
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
	return server
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

func commitBeforeChannelWrite(t *testing.T, writer *sql.DB, write func(context.Context, *sqlc.Queries) error, save func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := write(ctx, sqlc.New(tx)); err != nil {
		t.Fatal(err)
	}
	// The only writer connection is reserved. WAL readers still see the old state.
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
		t.Fatal("request did not finish after the writer commit")
		return nil
	}
}
