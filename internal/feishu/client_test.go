package feishu_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/feishu"
)

func TestUserReceivesReplyAfterRateLimit(t *testing.T) {
	// Given a platform that limits the first send.
	var tokens, sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			tokens.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
		case "/open-apis/im/v1/messages":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("missing tenant token")
			}
			if sends.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":99991400}`))
				return
			}
			var body map[string]any
			if err := json.UnmarshalRead(r.Body, &body); err != nil {
				t.Error(err)
			}
			if body["receive_id"] != "owner" || body["uuid"] != "delivery-1" {
				t.Errorf("unexpected message: %v", body)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"reply"}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := feishu.NewClient(feishu.Config{AppID: "app", AppSecret: "secret", APIURL: server.URL})
	// When dex sends the answer with a stable delivery ID.
	id, err := client.Send(t.Context(), "owner", "text", `{"text":"answer"}`, "delivery-1")
	if err != nil {
		t.Fatal(err)
	}
	// Then the retry succeeds and both attempts use one cached token.
	if id != "reply" || sends.Load() != 2 || tokens.Load() != 1 {
		t.Fatalf("id=%s sends=%d tokens=%d", id, sends.Load(), tokens.Load())
	}
}

func TestUserCancelsPlatformRequest(t *testing.T) {
	// Given a canceled request.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := feishu.NewClient(feishu.Config{AppID: "app", AppSecret: "secret", APIURL: "http://127.0.0.1:1"})
	// When dex attempts a send, then it returns without a retry delay.
	if _, err := client.Send(ctx, "owner", "text", `{"text":"answer"}`, "id"); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestUserRefreshesExpiredTenantToken(t *testing.T) {
	// Given a platform that rejects a cached token.
	var tokens, sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			n := tokens.Add(1)
			_ = json.MarshalWrite(w, map[string]any{"code": 0, "tenant_access_token": fmt.Sprint("token-", n), "expire": 7200})
			return
		}
		if sends.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":99991663}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-2" {
			t.Error("token was not refreshed")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"reply"}}`))
	}))
	defer server.Close()
	// When dex sends a reply, then a fresh token completes it.
	client := feishu.NewClient(feishu.Config{AppID: "app", AppSecret: "secret", APIURL: server.URL})
	if _, err := client.Send(t.Context(), "owner", "text", `{"text":"answer"}`, "stable"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 || sends.Load() != 2 {
		t.Fatalf("tokens=%d sends=%d", tokens.Load(), sends.Load())
	}
}

func TestUserPlatformFailureHasBoundedAttempts(t *testing.T) {
	for _, scenario := range []struct {
		name               string
		status, code, want int
	}{{"permanent", 400, 230001, 1}, {"temporary", 503, 1, 3}, {"API temporary", 200, 500, 3}} {
		t.Run(scenario.name, func(t *testing.T) {
			// Given a platform that refuses a message.
			var sends atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
					return
				}
				sends.Add(1)
				w.WriteHeader(scenario.status)
				_ = json.MarshalWrite(w, map[string]int{"code": scenario.code})
			}))
			defer server.Close()
			// When dex sends it, then permanent errors stop immediately and temporary errors stop at three attempts.
			client := feishu.NewClient(feishu.Config{AppID: "app", AppSecret: "secret", APIURL: server.URL})
			if _, err := client.Send(t.Context(), "owner", "text", `{"text":"answer"}`, "stable"); err == nil {
				t.Fatal("expected platform error")
			}
			if sends.Load() != int32(scenario.want) {
				t.Fatalf("attempts=%d want=%d", sends.Load(), scenario.want)
			}
		})
	}
}

func TestUserDownloadsAfterTemporaryFailureAndExpiredToken(t *testing.T) {
	// Given transient failures on both sides of a token refresh.
	var tokens, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			n := tokens.Add(1)
			_ = json.MarshalWrite(w, map[string]any{"code": 0, "tenant_access_token": fmt.Sprint("token-", n), "expire": 7200})
			return
		}
		n := gets.Add(1)
		want := "Bearer token-1"
		if n > 3 {
			want = "Bearer token-2"
		}
		if r.Header.Get("Authorization") != want {
			t.Errorf("authorization=%s want=%s", r.Header.Get("Authorization"), want)
		}
		switch n {
		case 1, 2, 4, 5:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":1}`))
		case 3:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":99991663}`))
		default:
			_, _ = w.Write([]byte("resource"))
		}
	}))
	defer server.Close()
	// When dex downloads, then each token has three attempts and the body streams.
	client := feishu.NewClient(feishu.Config{APIURL: server.URL})
	body, err := client.Download(t.Context(), "message", "key", "file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "resource" || gets.Load() != 6 || tokens.Load() != 2 {
		t.Fatalf("data=%q err=%v gets=%d tokens=%d", data, err, gets.Load(), tokens.Load())
	}
}
