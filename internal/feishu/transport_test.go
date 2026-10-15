package feishu

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestUserMessageIsAcknowledgedOnlyAfterAdmission(t *testing.T) {
	// Given two fragments from a local platform and a failed durable admission.
	admitted := make(chan string, 1)
	ack := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/callback/ws/endpoint" {
			_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]any{"URL": "ws://" + r.Host + "/ws?service_id=7", "ClientConfig": connectionConfig{ReconnectCount: 0, PingInterval: 10}}})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for i, payload := range []string{"hello ", "owner"} {
			f := frame{seq: 1, service: 7, method: 1, headers: []header{{"type", "event"}, {"sum", "2"}, {"seq", fmt.Sprint(i)}, {"message_id", "message"}}, payload: []byte(payload)}
			if err := conn.Write(r.Context(), websocket.MessageBinary, f.marshal()); err != nil {
				return
			}
		}
		for {
			_, b, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			f, err := parseFrame(b)
			if err != nil {
				return
			}
			if f.method == 1 {
				ack <- string(f.payload)
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := NewClient(Config{APIURL: server.URL, AppID: "app", AppSecret: "secret"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Listen(ctx, func(_ context.Context, b []byte) error {
			admitted <- string(b)
			return errors.New("storage unavailable")
		}, func(error) {})
	}()
	// When the complete event reaches admission, then the frame gets a retryable acknowledgement.
	select {
	case body := <-admitted:
		if body != "hello owner" {
			t.Fatal(body)
		}
	case <-ctx.Done():
		t.Fatal("no admission")
	}
	select {
	case body := <-ack:
		if body != `{"code":500}` {
			t.Fatal(body)
		}
	case <-ctx.Done():
		t.Fatal("no ack")
	}
	cancel()
	<-done
}

func TestUserReadsRichPostAndMentions(t *testing.T) {
	// Given a platform post with text, an image, and a bot mention.
	content := `{"zh_cn":{"title":"Title","content":[[{"tag":"text","text":"@_user_1 hello"},{"tag":"img","image_key":"image-key"}]]}}`
	event := map[string]any{"header": map[string]string{"event_type": "im.message.receive_v1"}, "event": map[string]any{"sender": map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "owner"}}, "message": map[string]any{"message_id": "message", "chat_type": "p2p", "message_type": "post", "content": content, "mentions": []any{map[string]string{"key": "@_user_1"}}}}}
	b, _ := json.Marshal(event)
	// When dex reads it, then it keeps the text and resource and removes the mention.
	m, err := ParseMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Title\n hello" || m.Sender != "owner" || len(m.Resources) != 1 || m.Resources[0].Key != "image-key" {
		t.Fatalf("%+v", m)
	}
}

func TestUserCreatesAppWithMinimumPermissions(t *testing.T) {
	// Given a device registration service.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify?code=scan","interval":2,"expires_in":300}`))
			return
		}
		_, _ = w.Write([]byte(`{"client_id":"app","client_secret":"secret","user_info":{"open_id":"owner","tenant_brand":"feishu"}}`))
	}))
	defer server.Close()
	registrar := Registrar{AccountsURL: server.URL}
	// When the owner completes the flow, then dex receives credentials and the owner identity.
	session, err := registrar.Begin(t.Context(), "feishu")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.QRURL, "addons=") || !strings.Contains(session.QRURL, "createOnly=true") {
		t.Fatal("missing setup parameters")
	}
	result, err := registrar.Poll(t.Context(), "feishu", session.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if result.AppID != "app" || result.UserInfo.OpenID != "owner" {
		t.Fatal("missing owner identity")
	}
}

func TestUserRegistersLarkFromFeishuAccount(t *testing.T) {
	// Given a registration response that directs the device flow to Lark.
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("device_code") != "device" {
			t.Error("device code changed across brands")
		}
		polls++
		if polls == 1 {
			_, _ = w.Write([]byte(`{"user_info":{"tenant_brand":"lark"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"client_id":"lark-app","client_secret":"secret","user_info":{"open_id":"lark-owner","tenant_brand":"lark"}}`))
	}))
	defer server.Close()
	// When dex polls, then it follows the brand fallback and keeps the owner identity.
	result, err := (Registrar{AccountsURL: server.URL}).Poll(t.Context(), "feishu", "device")
	if err != nil {
		t.Fatal(err)
	}
	if polls != 2 || result.UserInfo.OpenID != "lark-owner" || result.UserInfo.Brand != "lark" {
		t.Fatalf("polls=%d result=%+v", polls, result.UserInfo)
	}
}
