package settings_test

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

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/wechat"
)

func TestUserWechatCanceledActivationRetainsCandidate(t *testing.T) {
	// Given a real QR response canceled just before local activation.
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
	s := settings.New(q, nil, "agent", "", nil)
	activated := false
	s.ConfigureWechat(wechat.Options{APIURL: srv.URL}, conn, func(cfg *wechat.Config) {
		if cfg == nil {
			cancel()
		} else {
			activated = true
		}
	})
	login, err := s.BeginWechatLogin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// When stopping the previous runtime cancels the request, no settings can commit.
	view, err := s.PollWechatLogin(ctx, login.ID, "")
	if !errors.Is(err, context.Canceled) || view.Status == "confirmed" {
		t.Fatalf("canceled activation: %+v %v", view, err)
	}
	if _, err := q.GetWechatSettings(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial settings after cancellation: %v", err)
	}
	if activated {
		t.Fatal("canceled activation started a bot")
	}
	// Then a new request can activate the retained candidate without polling WeChat again.
	view, err = s.PollWechatLogin(t.Context(), login.ID, "")
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
			s := settings.New(sqlc.New(conn), nil, "agent", "", nil)
			s.ConfigureWechat(wechat.Options{APIURL: srv.URL}, conn, func(*wechat.Config) {})
			done := make(chan error, 1)
			go func() { _, err := s.BeginWechatLogin(t.Context()); done <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("QR request did not start")
			}
			// When a newer login or delete takes ownership, the older result cannot replace it.
			var current wechat.LoginView
			if replacement == "new login" {
				current, err = s.BeginWechatLogin(t.Context())
			} else {
				err = s.DeleteWechat(t.Context())
			}
			if err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case err := <-done:
				if !errors.Is(err, settings.ErrInvalid) {
					t.Fatalf("superseded start: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("old start did not finish")
			}
			if replacement == "new login" {
				view, err := s.PollWechatLogin(t.Context(), current.ID, "")
				if err != nil || view.Status != "wait" || view.ID != current.ID {
					t.Fatalf("current login replaced: %+v %v", view, err)
				}
			}
		})
	}
}
