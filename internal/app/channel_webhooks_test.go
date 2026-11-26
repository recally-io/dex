package app_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
)

const telegramHook = "/hooks/channels/telegram/default"
const feishuHook = "/hooks/channels/feishu/default"

func postChannelHook(t *testing.T, d *dex, path string, body []byte, headers map[string]string, want int) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != want {
		t.Fatalf("POST %s: got %d %s (%v), want %d", path, resp.StatusCode, raw, err, want)
	}
	return string(raw)
}

func TestUserVerifiesFeishuWebhook(t *testing.T) {
	t.Parallel()
	// Given a Feishu app configured for token-verified callbacks.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	var setup struct {
		PairingCode string `json:"pairingCode"`
	}
	d.must(t, http.MethodPut, "/api/agents/default/channels/feishu", map[string]any{
		"appId": "app", "appSecret": "app-secret", "brand": "feishu", "transport": "webhook",
		"webhook": map[string]string{"verificationToken": "verify-me"},
	}, &setup)
	// When Feishu verifies the URL, only its valid token gets the challenge back.
	postChannelHook(t, d, feishuHook, []byte(`{"type":"url_verification","token":"wrong","challenge":"proof"}`), nil, http.StatusUnauthorized)
	raw := postChannelHook(t, d, feishuHook, []byte(`{"type":"url_verification","token":"verify-me","challenge":"proof"}`), nil, http.StatusOK)
	if !strings.Contains(raw, `"challenge":"proof"`) {
		t.Fatalf("challenge response: %s", raw)
	}
	// A wrong event token cannot consume a valid pairing code. A group cannot pair.
	postChannelHook(t, d, feishuHook, feishuHookText(t, "pair", "owner", "p2p", "wrong", "/pair "+setup.PairingCode), nil, http.StatusUnauthorized)
	postChannelHook(t, d, feishuHook, feishuHookText(t, "group", "stranger", "group", "verify-me", "/pair "+setup.PairingCode), nil, http.StatusOK)
	postChannelHook(t, d, feishuHook, feishuHookText(t, "pair", "owner", "p2p", "verify-me", "/pair "+setup.PairingCode), nil, http.StatusOK)
	f.waitReply(t, "Paired.")
	old := time.Now().Add(-8 * 24 * time.Hour)
	postChannelHook(t, d, feishuHook, feishuHookTextAt(t, "hello", "owner", "p2p", "verify-me", "old replay", old), map[string]string{larkevent.EventRequestTimestamp: strconv.FormatInt(time.Now().Unix(), 10)}, http.StatusBadRequest)
	postChannelHook(t, d, feishuHook, feishuHookText(t, "hello", "owner", "p2p", "verify-me", "hello webhook"), nil, http.StatusOK)
	f.waitReply(t, "echo: hello webhook")
	_, view := d.call(t, http.MethodGet, "/api/agents/default/channels/feishu", nil)
	if !strings.Contains(view, `"transport":"webhook"`) || strings.Contains(view, "verify-me") {
		t.Fatalf("unexpected Feishu view: %s", view)
	}
}

func hookJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func telegramHookMessage(t *testing.T, id, user int64, text string) []byte {
	t.Helper()
	return hookJSON(t, map[string]any{"update_id": id, "message": map[string]any{
		"message_id": id, "from": map[string]int64{"id": user}, "chat": map[string]any{"id": 42, "type": "private"}, "text": text,
	}})
}

func TestUserSwitchesTelegramWebhookWithDurableRetriesAndRollback(t *testing.T) {
	t.Parallel()
	// Given a polling bot and a platform that can hold a media download open.
	f := newFakeTelegram(t)
	fileGate := make(chan struct{})
	f.fileGate = fileGate
	f.files["held.txt"] = "held download"
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	f.message(100, 42, 42, "default polling")
	waitTaskChannelText(t, f, "echo: default polling")
	postChannelHook(t, d, telegramHook, []byte(`{}`), nil, http.StatusNotFound)

	// When the owner enables the webhook, Telegram retains pending updates.
	const settings = "/api/agents/default/channels/telegram"
	const secret = "telegram-hook-secret"
	const endpoint = "https://hooks.example.test" + telegramHook
	webhook := map[string]any{"token": "", "allowedUsers": []int64{42}, "transport": "webhook",
		"webhook": map[string]string{"url": endpoint, "secretToken": secret}}
	polling := map[string]any{"token": "", "allowedUsers": []int64{42}, "transport": "polling"}
	d.must(t, http.MethodPut, settings, webhook, nil)
	f.nextEvent(t, "setWebhook")
	f.mu.Lock()
	registered := f.webhook
	f.mu.Unlock()
	if registered.URL != endpoint || registered.Secret != secret || registered.Drop || registered.Max != 1 || !slices.Equal(registered.Updates, []string{"message", "edited_message", "callback_query"}) {
		t.Fatalf("incorrect registration: %+v", registered)
	}
	_, view := d.call(t, http.MethodGet, "/api/agents/default/channels", nil)
	if !strings.Contains(view, `"transport":"webhook"`) || strings.Contains(view, secret) {
		t.Fatalf("unexpected Telegram view: %s", view)
	}

	const header = "X-Telegram-Bot-Api-Secret-Token"
	headers := map[string]string{header: secret}
	postChannelHook(t, d, telegramHook, telegramHookMessage(t, 201, 42, "forged"), map[string]string{header: "wrong"}, http.StatusUnauthorized)
	postChannelHook(t, d, telegramHook, []byte(`{}`), headers, http.StatusBadRequest)
	postChannelHook(t, d, telegramHook, []byte(`{"update_id":200,"message":{"message_id":200,"from":{"id":42},"chat":{"id":42,"type":"private"},"document":{"file_id":"held.txt","file_name":"held.txt"}}}`), headers, http.StatusOK)
	f.nextEvent(t, "download")

	// A failed storage write cannot ACK. An accepted update behind the held
	// download survives a restart even though the agent has not seen it.
	conn := controlWriteDB(t, dir)
	if _, err := conn.ExecContext(t.Context(), `CREATE TRIGGER reject_webhook BEFORE INSERT ON channel_webhook_queue WHEN NEW.event_id = '201' BEGIN SELECT RAISE(FAIL, 'intake unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	msg := telegramHookMessage(t, 201, 42, "durable webhook")
	postChannelHook(t, d, telegramHook, msg, headers, http.StatusServiceUnavailable)
	if _, err := conn.ExecContext(t.Context(), `DROP TRIGGER reject_webhook`); err != nil {
		t.Fatal(err)
	}
	postChannelHook(t, d, telegramHook, msg, headers, http.StatusOK)
	assertWebhookInputs(t, dir, "telegram:123:201", 0)
	d.stop()
	close(fileGate)
	registerGate := make(chan struct{})
	f.mu.Lock()
	f.registerGate = registerGate
	f.mu.Unlock()
	d = startDex(t, dir, f.srv.URL)
	f.nextEvent(t, "registerWaiting")
	postChannelHook(t, d, telegramHook, telegramHookMessage(t, 201, 42, "changed retry"), headers, http.StatusOK)
	waitTaskChannelText(t, f, "echo: durable webhook")
	close(registerGate)
	f.nextEvent(t, "setWebhook")
	postChannelHook(t, d, telegramHook, telegramHookMessage(t, 202, 99, "unauthorized"), headers, http.StatusOK)
	postChannelHook(t, d, telegramHook, telegramHookMessage(t, 203, 42, "webhook barrier"), headers, http.StatusOK)
	waitTaskChannelText(t, f, "echo: webhook barrier")
	assertWebhookInputs(t, dir, "telegram:123:201", 1)
	assertWebhookInputs(t, dir, "telegram:123:202", 0)

	// A failed teardown cannot start polling. A successful rollback restores
	// polling and can read updates still pending at Telegram.
	f.mu.Lock()
	f.failures["deleteWebhook"] = []platformFailure{{HTTPStatus: http.StatusServiceUnavailable}}
	f.mu.Unlock()
	d.must(t, http.MethodPut, settings, polling, nil, http.StatusInternalServerError)
	f.nextEvent(t, "setWebhook")
	postChannelHook(t, d, telegramHook, msg, headers, http.StatusOK)
	f.message(202, 42, 42, "replayed across transport")
	f.message(204, 42, 42, "pending for rollback")
	d.must(t, http.MethodPut, settings, polling, nil)
	f.nextEvent(t, "deleteWebhook")
	postChannelHook(t, d, telegramHook, msg, headers, http.StatusNotFound)
	waitTaskChannelText(t, f, "echo: pending for rollback")
	assertWebhookInputs(t, dir, "telegram:123:202", 0)
	if conflicts := f.webhookPolls.Load(); conflicts != 0 {
		t.Fatalf("getUpdates used %d times while webhook was registered", conflicts)
	}
}

func TestUserKeepsPendingAndRecentWebhookEventsAfterCleanup(t *testing.T) {
	t.Parallel()
	// Given handled events of different ages and an old pending event.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := controlWriteDB(t, dir)
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour).UnixMilli()
	if _, err := conn.ExecContext(t.Context(), `
INSERT INTO channel_webhook_queue (platform, account, event_id, agent_id, received_at, payload)
VALUES ('telegram', '123', 'recent', 'default', ?, NULL),
       ('telegram', '123', 'skew-margin', 'default', ?, NULL),
       ('telegram', '123', 'handled', 'default', ?, NULL),
       ('telegram', '123', 'pending', 'default', ?, CAST('{}' AS BLOB))`, now.UnixMilli(), now.Add(-7*24*time.Hour-time.Minute).UnixMilli(), old, old); err != nil {
		t.Fatal(err)
	}
	d.stop()

	// On restart the cleanup job removes only the old handled event.
	_ = startDex(t, dir, "")
	eventually(t, "old handled webhook row pruned", func() (bool, any) {
		var events string
		err := conn.QueryRowContext(t.Context(), `SELECT group_concat(event_id ORDER BY event_id) FROM channel_webhook_queue`).Scan(&events)
		if err != nil {
			t.Fatal(err)
		}
		return events == "pending,recent,skew-margin", events
	})
}

func assertWebhookInputs(t *testing.T, dir, source string, want int) {
	t.Helper()
	var count int
	if err := taskResultDB(t, dir).QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE source_key = ?`, source).Scan(&count); err != nil || count != want {
		t.Fatalf("source %s: got %d inputs (%v), want %d", source, count, err, want)
	}
}

func feishuHookText(t *testing.T, id, user, chatType, token, text string) []byte {
	t.Helper()
	return feishuHookTextAt(t, id, user, chatType, token, text, time.Now())
}

func feishuHookTextAt(t *testing.T, id, user, chatType, token, text string, created time.Time) []byte {
	t.Helper()
	return hookJSON(t, map[string]any{
		"schema": "2.0", "header": map[string]string{"event_type": "im.message.receive_v1", "event_id": "event-" + id, "app_id": "app", "token": token, "create_time": strconv.FormatInt(created.UnixMilli(), 10)},
		"event": map[string]any{
			"sender":  map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": user}},
			"message": map[string]string{"message_id": id, "chat_type": chatType, "message_type": "text", "content": string(hookJSON(t, map[string]string{"text": text}))},
		},
	})
}

const feishuTestEncryptKey = "encrypted-feishu-test"

func encryptedFeishuHook(t *testing.T, body []byte) []byte {
	t.Helper()
	digest := sha256.Sum256([]byte(feishuTestEncryptKey))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(body)%aes.BlockSize
	body = append(bytes.Clone(body), bytes.Repeat([]byte{byte(padding)}, padding)...)
	iv := make([]byte, aes.BlockSize)
	_, _ = rand.Read(iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(body, body)
	return hookJSON(t, map[string]string{"encrypt": base64.StdEncoding.EncodeToString(append(iv, body...))})
}

func TestUserSwitchesFeishuToEncryptedWebhookAndBack(t *testing.T) {
	t.Parallel()
	// Given the default WebSocket transport and an existing paired owner.
	f := newFakeFeishu(t)
	dir := t.TempDir()
	d := startFeishu(t, f, dir)
	pairFeishu(t, d, f)
	postChannelHook(t, d, feishuHook, []byte(`{}`), nil, http.StatusNotFound)
	f.mu.Lock()
	wsRequests := f.startupRequests
	f.mu.Unlock()

	// When the owner switches, only encrypted and signed events enter the inbox.
	const settings = "/api/agents/default/channels/feishu"
	d.must(t, http.MethodPut, settings, map[string]any{"appId": "app", "appSecret": "", "brand": "feishu", "transport": "webhook",
		"webhook": map[string]string{"verificationToken": "verify-me", "encryptKey": feishuTestEncryptKey}}, nil)
	plaintextChallenge := []byte(`{"type":"url_verification","token":"verify-me","challenge":"encrypted-proof"}`)
	postChannelHook(t, d, feishuHook, plaintextChallenge, nil, http.StatusUnauthorized)
	postChannelHook(t, d, feishuHook, encryptedFeishuHook(t, []byte(`} invalid {`)), nil, http.StatusUnauthorized)
	body := encryptedFeishuHook(t, plaintextChallenge)
	if raw := postChannelHook(t, d, feishuHook, body, nil, http.StatusOK); !strings.Contains(raw, `"challenge":"encrypted-proof"`) {
		t.Fatalf("encrypted challenge: %s", raw)
	}
	headers := map[string]string{larkevent.EventRequestNonce: "nonce"}
	sign := func(body []byte) {
		headers[larkevent.EventSignature] = larkevent.Signature(headers[larkevent.EventRequestTimestamp], headers[larkevent.EventRequestNonce], feishuTestEncryptKey, string(body))
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	body = encryptedFeishuHook(t, feishuHookText(t, "secure", "owner", "p2p", "verify-me", "signed webhook"))
	headers[larkevent.EventRequestTimestamp] = strconv.FormatInt(old.Unix(), 10)
	sign(body)
	postChannelHook(t, d, feishuHook, body, headers, http.StatusBadRequest)
	headers[larkevent.EventRequestTimestamp] = strconv.FormatInt(time.Now().Unix(), 10)
	oldBody := encryptedFeishuHook(t, feishuHookTextAt(t, "secure", "owner", "p2p", "verify-me", "old event", old))
	sign(oldBody)
	postChannelHook(t, d, feishuHook, oldBody, headers, http.StatusBadRequest)

	// A signed time beyond the skew is rejected. A small clock difference is allowed.
	future := time.Now().Add(6 * time.Minute)
	headers[larkevent.EventRequestTimestamp] = strconv.FormatInt(future.Unix(), 10)
	sign(body)
	postChannelHook(t, d, feishuHook, body, headers, http.StatusBadRequest)
	nearFuture := time.Now().Add(30 * time.Second)
	headers[larkevent.EventRequestTimestamp] = strconv.FormatInt(nearFuture.Unix(), 10)
	futureBody := encryptedFeishuHook(t, feishuHookTextAt(t, "secure", "owner", "p2p", "verify-me", "future event", future))
	sign(futureBody)
	postChannelHook(t, d, feishuHook, futureBody, headers, http.StatusBadRequest)
	body = encryptedFeishuHook(t, feishuHookTextAt(t, "secure", "owner", "p2p", "verify-me", "signed webhook", nearFuture))
	postChannelHook(t, d, feishuHook, body, nil, http.StatusUnauthorized)
	headers[larkevent.EventSignature] = "bad-signature"
	postChannelHook(t, d, feishuHook, body, headers, http.StatusUnauthorized)
	sign(body)
	postChannelHook(t, d, feishuHook, body, headers, http.StatusOK)
	f.waitReply(t, "echo: signed webhook")
	d.stop()

	// A retry with the same event ID and a different message ID cannot enter
	// the agent after restart. The IDs are distinct in the platform protocol.
	d = startFeishu(t, f, dir)
	retry := bytes.Replace(feishuHookText(t, "changed", "owner", "p2p", "verify-me", "changed retry"), []byte(`"event-changed"`), []byte(`"event-secure"`), 1)
	body = encryptedFeishuHook(t, retry)
	sign(body)
	postChannelHook(t, d, feishuHook, body, headers, http.StatusOK)
	body = encryptedFeishuHook(t, feishuHookText(t, "barrier", "owner", "p2p", "verify-me", "Feishu barrier"))
	sign(body)
	postChannelHook(t, d, feishuHook, body, headers, http.StatusOK)
	f.waitReply(t, "echo: Feishu barrier")
	assertWebhookInputs(t, dir, "feishu:app:secure", 1)
	assertWebhookInputs(t, dir, "feishu:app:changed", 0)
	f.mu.Lock()
	gotWSRequests := f.startupRequests
	f.mu.Unlock()
	if gotWSRequests != wsRequests {
		t.Fatalf("WebSocket requested during webhook mode: %d before, %d after", wsRequests, gotWSRequests)
	}

	// Rolling back starts the WebSocket without making the HTTP endpoint active.
	d.must(t, http.MethodPut, settings, map[string]any{"appId": "app", "appSecret": "", "brand": "feishu", "transport": "websocket"}, nil)
	postChannelHook(t, d, feishuHook, body, headers, http.StatusNotFound)
	f.events <- retry
	waitSignal(t, f.ack, "WebSocket ACK for replay across transports")
	f.text(t, "after-rollback", "owner", "p2p", "WebSocket restored")
	f.waitReply(t, "echo: WebSocket restored")
	assertWebhookInputs(t, dir, "feishu:app:changed", 0)
}
