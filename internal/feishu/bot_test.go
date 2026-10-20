package feishu

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/channel"
)

func TestUserFinalPatchRetriesWithoutSecondVisibleAnswer(t *testing.T) {
	for _, failure := range []string{"503", "lost response"} {
		t.Run(failure, func(t *testing.T) {
			// Given a preview and a server that applies PATCH but cannot confirm success.
			var mu sync.Mutex
			visible := map[string]string{}
			uuids := map[string]string{}
			patches := 0
			uncertain := true
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
					return
				}
				var body struct {
					Content string `json:"content"`
					UUID    string `json:"uuid"`
				}
				if err := json.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodPatch {
					id := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
					visible[id] = body.Content
					patches++
					if uncertain {
						if failure == "lost response" {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = conn.Close()
							return
						}
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(`{"code":1}`))
					} else {
						_, _ = w.Write([]byte(`{"code":0}`))
					}
					return
				}
				id := uuids[body.UUID]
				if id == "" {
					id = fmt.Sprintf("remote-%d", len(visible)+1)
					uuids[body.UUID] = id
					visible[id] = body.Content
				}
				_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]string{"message_id": id}})
			}))
			defer server.Close()
			ctx := t.Context()
			manager := channel.New(ctx, nil, nil, "")
			bot := New(Config{APIURL: server.URL, OwnerID: "owner"}, manager, nil, nil, nil)
			previewID, err := bot.client.Send(ctx, "owner", "interactive", CardContent("Thinking", false), deliveryUUID("submission:preview", 0))
			if err != nil {
				t.Fatal(err)
			}
			_, cancel := context.WithCancel(ctx)
			defer cancel()
			bot.previews["submission"] = &preview{messageID: previewID, cancel: cancel}
			// When final delivery sees an uncertain PATCH result, it must keep that message identity.
			err = bot.Send(ctx, "feishu:owner", "submission", 0, "one final answer")
			if err == nil {
				t.Error("uncertain PATCH was confirmed as delivered")
			}
			mu.Lock()
			uncertain = false
			mu.Unlock()
			// Then durable delivery can retry the same remote card after recovery.
			if err := bot.Send(ctx, "feishu:owner", "submission", 0, "one final answer"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if patches != 4 {
				t.Errorf("PATCH attempts=%d, want three uncertain attempts and one confirmed retry", patches)
			}
			answers := 0
			for _, content := range visible {
				if strings.Contains(content, "one final answer") {
					answers++
				}
			}
			t.Logf("PATCH attempts=%d, visible final answers=%d, Send error=%v", patches, answers, err)
			if answers != 1 {
				t.Fatalf("one reply became %d visible final cards after uncertain PATCH results", answers)
			}
		})
	}
}
