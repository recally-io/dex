package wechat_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/recally-io/dex/internal/channel/wechat"
)

func TestUserWechatRefreshRemainsUsableAfterFiveMinutes(t *testing.T) {
	t.Parallel()
	// Given a real QR boundary that expires the initial code and confirms its replacement.
	var polls atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_ = json.MarshalWrite(w, map[string]string{"qrcode": "code", "qrcode_img_content": "QR"})
		case "/ilink/bot/get_qrcode_status":
			if polls.Add(1) == 1 {
				_ = json.MarshalWrite(w, map[string]string{"status": "expired"})
			} else {
				_ = json.MarshalWrite(w, map[string]string{"status": "confirmed", "bot_token": "token", "ilink_bot_id": "bot", "ilink_user_id": "owner", "baseurl": srv.URL})
			}
		}
	}))
	defer srv.Close()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	conn, q, id := setupDB(t)
	synctest.Test(t, func(t *testing.T) {
		s := wechat.NewSetup(q, conn, new(sync.Mutex), wechat.Options{APIURL: srv.URL, HTTPClient: client}, func(string, *wechat.Config) {}, nil)
		login, err := s.Begin(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		// When more than five minutes elapse, remote expiry can still replace the QR code.
		time.Sleep(5*time.Minute + time.Second)
		view, err := s.Poll(t.Context(), id, login.ID, "")
		if err != nil || !view.Active || view.Status != "expired" || polls.Load() != 1 {
			t.Fatalf("refresh: %+v err=%v remote polls=%d", view, err, polls.Load())
		}
		// Then the replacement can confirm and activate the account.
		view, err = s.Poll(t.Context(), id, login.ID, "")
		if err != nil || view.Status != "confirmed" || view.Active {
			t.Fatalf("activation: %+v %v", view, err)
		}
	})
}
