package wechat_test

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
	conn, q, id := setupDB(t)
	if err := q.SetWechatSettings(t.Context(), sqlc.SetWechatSettingsParams{BotID: id, Token: "old", BaseUrl: srv.URL, WechatBotID: "previous", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	var running *wechat.Config
	s := wechat.NewSetup(q, conn, new(sync.Mutex), wechat.Options{APIURL: srv.URL}, func(_ string, cfg *wechat.Config) {
		running = cfg
		if cfg == nil {
			cancel()
		}
	}, nil)
	login, err := s.Begin(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	// When stopping the previous runtime cancels the request, no settings can commit.
	view, err := s.Poll(ctx, id, login.ID, "")
	if !errors.Is(err, context.Canceled) || view.Status == "confirmed" {
		t.Fatalf("canceled activation: %+v %v", view, err)
	}
	saved, err := q.GetWechatSettings(t.Context(), id)
	if err != nil || saved.WechatBotID != "previous" || running == nil || running.BotID != "previous" {
		t.Fatal("canceled activation did not restore the previous account")
	}
	// Then a new request can activate the retained candidate without polling WeChat again.
	view, err = s.Poll(t.Context(), id, login.ID, "")
	activated := running != nil && running.BotID == "bot"
	if err != nil || view.Status != "confirmed" || view.Active || !activated || polls.Load() != 1 {
		t.Fatalf("retry: %+v err=%v activated=%v remote polls=%d", view, err, activated, polls.Load())
	}
	if _, err := q.GetWechatRuntime(t.Context(), sqlc.GetWechatRuntimeParams{BotID: "bot", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	// A retry after success returns the same confirmation without activating again.
	running = nil
	replayed, err := s.Poll(t.Context(), id, login.ID, "")
	if err != nil || replayed != view || running != nil || polls.Load() != 1 {
		t.Fatalf("confirmation retry: %+v err=%v start requested=%t remote polls=%d", replayed, err, running != nil, polls.Load())
	}
}

func TestUserWechatNewLoginAndDeleteInvalidateWaitingStart(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"new login", "delete"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
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
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			conn, q, id := setupDB(t)
			s := wechat.NewSetup(q, conn, new(sync.Mutex), wechat.Options{APIURL: srv.URL}, func(string, *wechat.Config) {}, nil)
			done := make(chan error, 1)
			go func() { _, err := s.Begin(t.Context(), id); done <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("QR request did not start")
			}
			// When a newer login or delete takes ownership, the older result cannot replace it.
			var current wechat.LoginView
			var err error
			if replacement == "new login" {
				current, err = s.Begin(t.Context(), id)
			} else {
				err = s.Delete(t.Context(), id)
			}
			if err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case err := <-done:
				if !errors.Is(err, wechat.ErrInvalid) {
					t.Fatalf("superseded start: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("old start did not finish")
			}
			if replacement == "new login" {
				view, err := s.Poll(t.Context(), id, current.ID, "")
				if err != nil || view.Status != "wait" || view.ID != current.ID {
					t.Fatalf("current login replaced: %+v %v", view, err)
				}
			}
		})
	}
}

func TestUserWechatDeletionStopsBeforeForgettingCredentials(t *testing.T) {
	t.Parallel()
	// Given a connected account whose runtime is stopped at the platform boundary.
	ctx := t.Context()
	conn, q, id := setupDB(t)
	if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{BotID: id, Token: "old", BaseUrl: "https://example.com", WechatBotID: "bot", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	var stoppedWithOwner string
	s := wechat.NewSetup(q, conn, new(sync.Mutex), wechat.Options{}, func(_ string, cfg *wechat.Config) {
		if cfg == nil {
			row, err := q.GetWechatSettings(ctx, id)
			if err == nil {
				stoppedWithOwner = row.Owner
			}
		}
	}, nil)
	// When the owner disconnects, the runtime must have stopped while authorization existed.
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if stoppedWithOwner != "owner" {
		t.Fatalf("runtime stopped after authorization was removed: owner=%q", stoppedWithOwner)
	}
	if _, err := q.GetWechatSettings(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("account still connected: %v", err)
	}
}

func TestUserWechatDeleteFailureRestartsAccount(t *testing.T) {
	t.Parallel()
	// Given a connected account and a local storage failure when it is deleted.
	ctx := t.Context()
	conn, q, id := setupDB(t)
	if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{BotID: id, Token: "old", BaseUrl: "https://example.com", WechatBotID: "bot", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_wechat_delete BEFORE DELETE ON wechat_settings BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	var running *wechat.Config
	s := wechat.NewSetup(q, conn, new(sync.Mutex), wechat.Options{}, func(_ string, cfg *wechat.Config) { running = cfg }, nil)
	// When deletion fails, the previous account remains usable.
	if err := s.Delete(ctx, id); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	if running == nil || running.BotID != "bot" || running.Owner != "owner" {
		t.Fatalf("account not restarted: %+v", running)
	}
	if _, err := q.GetWechatSettings(ctx, id); err != nil {
		t.Fatalf("credentials lost: %v", err)
	}
}

func setupDB(t *testing.T) (*sql.DB, *sqlc.Queries, string) {
	t.Helper()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	const id = "default"
	if err := q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: id, Name: "Dex"}); err != nil {
		t.Fatal(err)
	}
	return conn, q, id
}
