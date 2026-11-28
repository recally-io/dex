package bot_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserWechatCanceledActivationRetainsCandidate(t *testing.T) {
	t.Parallel()
	// Given a connected account and a QR response canceled just before local activation.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var polls atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ilink/bot/get_bot_qrcode" {
			_ = json.MarshalWrite(w, map[string]string{"qrcode": "code", "qrcode_img_content": "QR"})
			return
		}
		polls.Add(1)
		_ = json.MarshalWrite(w, map[string]string{"status": "confirmed", "bot_token": "token", "ilink_bot_id": "bot", "ilink_user_id": "owner", "baseurl": srv.URL})
	}))
	defer srv.Close()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	q := sqlc.New(conn)
	if err := q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: "default", Name: "Dex"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetWechatSettings(t.Context(), sqlc.SetWechatSettingsParams{BotID: "default", Token: "old", BaseUrl: srv.URL, WechatBotID: "previous", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	s := bot.New(q, conn, new(sync.Mutex), nil, "", nil)
	var running *wechat.Config
	s.ConfigureWechat(wechat.Options{APIURL: srv.URL}, func(_ string, cfg *wechat.Config) {
		running = cfg
		if cfg == nil {
			cancel()
		}
	})
	login, err := s.BeginWechatLogin(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	// When stopping the previous runtime cancels the request, no settings can commit.
	view, err := s.PollWechatLogin(ctx, "default", login.ID, "")
	if !errors.Is(err, context.Canceled) || view.Status == "confirmed" {
		t.Fatalf("canceled activation: %+v %v", view, err)
	}
	saved, err := q.GetWechatSettings(t.Context(), "default")
	if err != nil || saved.WechatBotID != "previous" || running == nil || running.BotID != "previous" {
		t.Fatal("canceled activation did not restore the previous account")
	}
	// Then a new request can activate the retained candidate without polling WeChat again.
	view, err = s.PollWechatLogin(t.Context(), "default", login.ID, "")
	activated := running != nil && running.BotID == "bot"
	if err != nil || view.Status != "confirmed" || view.Active || !activated || polls.Load() != 1 {
		t.Fatalf("retry: %+v err=%v activated=%v remote polls=%d", view, err, activated, polls.Load())
	}
	if _, err := q.GetWechatRuntime(t.Context(), sqlc.GetWechatRuntimeParams{BotID: "bot", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
}

func TestUserWechatNewLoginAndDeleteInvalidateWaitingStart(t *testing.T) {
	for _, replacement := range []string{"new login", "delete"} {
		t.Run(replacement, func(t *testing.T) {
			// Given an initial QR request held at the protocol boundary.
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ilink/bot/get_qrcode_status" {
					_ = json.MarshalWrite(w, map[string]string{"status": "wait"})
					return
				}
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				_ = json.MarshalWrite(w, map[string]string{"qrcode": "code", "qrcode_img_content": "QR"})
			}))
			defer srv.Close()
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			s := bot.New(sqlc.New(conn), conn, new(sync.Mutex), nil, "", nil)
			s.ConfigureWechat(wechat.Options{APIURL: srv.URL}, func(string, *wechat.Config) {})
			done := make(chan error, 1)
			go func() { _, err := s.BeginWechatLogin(t.Context(), "default"); done <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("QR request did not start")
			}
			// When a newer login or delete takes ownership, the older result cannot replace it.
			var current wechat.LoginView
			if replacement == "new login" {
				current, err = s.BeginWechatLogin(t.Context(), "default")
			} else {
				err = s.DeleteWechat(t.Context(), "default")
			}
			if err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case err := <-done:
				if !errors.Is(err, bot.ErrInvalid) {
					t.Fatalf("superseded start: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("old start did not finish")
			}
			if replacement == "new login" {
				view, err := s.PollWechatLogin(t.Context(), "default", current.ID, "")
				if err != nil || view.Status != "wait" || view.ID != current.ID {
					t.Fatalf("current login replaced: %+v %v", view, err)
				}
			}
		})
	}
}

func TestUserWechatDeletionStopsBeforeForgettingCredentials(t *testing.T) {
	// Given a connected account whose runtime is stopped at the platform boundary.
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err := q.CreateBotIfMissing(ctx, sqlc.CreateBotIfMissingParams{ID: "default", Name: "Dex"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{BotID: "default", Token: "old", BaseUrl: "https://example.com", WechatBotID: "bot", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	s := bot.New(q, conn, new(sync.Mutex), nil, "", nil)
	var stoppedWithOwner string
	s.ConfigureWechat(wechat.Options{}, func(_ string, cfg *wechat.Config) {
		if cfg == nil {
			row, err := q.GetWechatSettings(ctx, "default")
			if err == nil {
				stoppedWithOwner = row.Owner
			}
		}
	})
	// When the owner disconnects, the runtime must have stopped while authorization existed.
	if err := s.DeleteWechat(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if stoppedWithOwner != "owner" {
		t.Fatalf("runtime stopped after authorization was removed: owner=%q", stoppedWithOwner)
	}
	if _, err := q.GetWechatSettings(ctx, "default"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("account still connected: %v", err)
	}
}

func TestUserWechatDeleteFailureRestartsAccount(t *testing.T) {
	// Given a connected account and a local storage failure when it is deleted.
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err := q.CreateBotIfMissing(ctx, sqlc.CreateBotIfMissingParams{ID: "default", Name: "Dex"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{BotID: "default", Token: "old", BaseUrl: "https://example.com", WechatBotID: "bot", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_wechat_delete BEFORE DELETE ON wechat_settings BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	s := bot.New(q, conn, new(sync.Mutex), nil, "", nil)
	var running *wechat.Config
	s.ConfigureWechat(wechat.Options{}, func(_ string, cfg *wechat.Config) { running = cfg })
	// When deletion fails, the previous account remains usable.
	if err := s.DeleteWechat(ctx, "default"); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	if running == nil || running.BotID != "bot" || running.Owner != "owner" {
		t.Fatalf("account not restarted: %+v", running)
	}
	if _, err := q.GetWechatSettings(ctx, "default"); err != nil {
		t.Fatalf("credentials lost: %v", err)
	}
}
