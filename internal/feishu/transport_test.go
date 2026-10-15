package feishu

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	content := `{"zh_cn":{"title":"Title","content":[[{"tag":"text","text":"@_user_1 hello"},{"tag":"img","image_key":"image-key"}],[{"tag":"code_block","language":"GO","text":"return 0"}]]}}`
	event := map[string]any{"header": map[string]string{"event_type": "im.message.receive_v1"}, "event": map[string]any{"sender": map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "owner"}}, "message": map[string]any{"message_id": "message", "chat_type": "p2p", "message_type": "post", "content": content, "mentions": []any{map[string]string{"key": "@_user_1"}}}}}
	b, _ := json.Marshal(event)
	// When dex reads it, then it keeps the text and resource and removes the mention.
	m, err := ParseMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Title\n hello\n\n```GO\nreturn 0\n```" || m.Sender != "owner" || len(m.Resources) != 1 || m.Resources[0].Key != "image-key" {
		t.Fatalf("%+v", m)
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

func TestUserReconnectsBeforePlatformRetryInterval(t *testing.T) {
	// Given a platform with a 120-second retry interval and no jitter.
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/callback/ws/endpoint" {
			_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]any{"URL": "ws://" + r.Host + "/ws?service_id=7", "ClientConfig": connectionConfig{ReconnectCount: -1, ReconnectInterval: 120, PingInterval: 1}}})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		connections.Add(1)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	// When the socket closes, then the first reconnect starts within two seconds.
	_ = NewClient(Config{APIURL: server.URL}).Listen(ctx, func(context.Context, []byte) error { return nil }, func(error) {
		if connections.Load() >= 2 {
			cancel()
		}
	})
	if connections.Load() < 2 {
		t.Fatalf("first reconnect waited for the retry interval: connections=%d", connections.Load())
	}
}

func TestUserReconnectsWhenPlatformStopsResponding(t *testing.T) {
	// Given a socket that accepts pings but sends no data or pong.
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/callback/ws/endpoint" {
			_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]any{"URL": "ws://" + r.Host + "/ws?service_id=7", "ClientConfig": connectionConfig{ReconnectCount: -1, ReconnectInterval: 1, PingInterval: 1}}})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		connections.Add(1)
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// When two heartbeat intervals pass, then dex replaces the silent socket.
	_ = NewClient(Config{APIURL: server.URL}).Listen(ctx, func(context.Context, []byte) error { return nil }, func(error) {
		if connections.Load() >= 2 {
			cancel()
		}
	})
	if connections.Load() < 2 {
		t.Fatalf("silent connection stayed open: connections=%d", connections.Load())
	}
}

func TestUserDownloadsBodyBeyondHeaderTimeout(t *testing.T) {
	// Given a server that sends headers immediately and a body after 31 seconds.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		timer := time.NewTimer(31 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = w.Write([]byte("slow resource"))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	// When the caller streams the body, then no 30-second client timeout cuts it off.
	body, err := NewClient(Config{APIURL: server.URL}).Download(ctx, "message", "key", "file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "slow resource" {
		t.Fatalf("download=%q err=%v", data, err)
	}
}

func TestUserDownloadHeaderWaitIsBounded(t *testing.T) {
	// Given a platform that sends no resource headers.
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
			return
		}
		gets.Add(1)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient(Config{APIURL: server.URL})
	// Shorten only the platform header boundary; body streaming has no client timeout.
	client.http.Transport.(*http.Transport).ResponseHeaderTimeout = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	// When headers never arrive, then the client exhausts its three attempts.
	if body, err := client.Download(ctx, "message", "key", "file"); err == nil {
		_ = body.Close()
		t.Fatal("download did not time out")
	}
	if gets.Load() != 3 || ctx.Err() != nil {
		t.Fatalf("gets=%d context=%v", gets.Load(), ctx.Err())
	}
}
