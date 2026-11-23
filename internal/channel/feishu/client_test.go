package feishu

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/channel"
)

func TestUserFeishuReactionDoesNotReplayOnRedirect(t *testing.T) {
	// Given a platform that redirects a reaction mutation.
	var replays atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
		case "/open-apis/im/v1/messages/om_original/reactions":
			http.Redirect(w, r, "/replay", http.StatusTemporaryRedirect)
		case "/replay":
			replays.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"data":{"reaction_id":"duplicate"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	bot := New(Config{AppID: "cli_fake", AppSecret: "fake", APIURL: server.URL}, nil, nil, nil, nil)

	// When Dex adds a status reaction.
	id, err := bot.React(t.Context(), "feishu:owner", "om_original", "", channel.ReactionQueued)

	// Then the redirect must not cause another POST or produce a confirmed ID.
	if got := replays.Load(); got != 0 || id != "" || err == nil {
		t.Fatalf("redirected mutations=%d, reaction=%q, err=%v; want no replay or confirmation", got, id, err)
	}
}

func TestUserFeishuUnconfirmedCreateDoesNotSucceed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing ID", `{"code":0,"data":null}`, http.StatusOK},
		{"error status with ID", `{"code":0,"data":{"message_id":"wrong"}}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a platform that responds without confirming a send.
			var creates atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
					return
				}
				creates.Add(1)
				var data map[string]string
				if err := json.UnmarshalRead(r.Body, &data); err != nil || data["uuid"] != "stable-id" || r.URL.Query().Get("receive_id_type") != "open_id" || r.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("invalid message create: method=%s query=%s payload=%v err=%v", r.Method, r.URL.RawQuery, data, err)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)

			// When Dex sends once with a stable UUID.
			c := newClient(Config{AppID: "cli_fake", AppSecret: "fake", APIURL: server.URL})
			id, err := c.Send(t.Context(), "owner", "interactive", "{}", "stable-id")

			// Then Dex does not report a platform confirmation or replay the send.
			if got := creates.Load(); got != 1 || id != "" || err == nil {
				t.Fatalf("creates=%d, message=%q, err=%v; want one attempt without confirmation", got, id, err)
			}
		})
	}
}
