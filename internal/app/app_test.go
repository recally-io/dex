package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/dex/internal/app"
	"github.com/vaayne/dex/internal/db/sqlc"
)

// These tests run the real embedded worker with pi-ai's faux provider: model faux/echo answers
// "echo: <message>", and faux/shout answers "shout: <message>". They do not prove that a real
// provider works.

type dex struct {
	app *app.App
	srv *httptest.Server
}

// startDex starts dex with its own data directory and serves its handler. telegramURL is the Bot
// API URL, or empty.
func startDex(t *testing.T, dataDir, telegramURL string) *dex {
	t.Helper()
	a, err := app.Start(t.Context(), app.Config{
		DataDir:        dataDir,
		CacheDir:       t.TempDir(),
		WorkerEnv:      []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "DEX_FAKE_MODEL=1"},
		TelegramAPIURL: telegramURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &dex{app: a, srv: httptest.NewServer(a.Handler())}
	t.Cleanup(d.stop)
	return d
}

func (d *dex) stop() {
	d.srv.Close()
	_ = d.app.Close()
}

// call sends a JSON request and returns the status and the body.
func (d *dex) call(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, d.srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// must sends a request that must succeed, and decodes the response into out when out is not nil.
func (d *dex) must(t *testing.T, method, path string, body, out any) {
	t.Helper()
	status, raw := d.call(t, method, path, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			t.Fatal(err)
		}
	}
}

func (d *dex) setModel(t *testing.T, modelID string) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/settings/agent", map[string]string{"provider": "faux", "modelId": modelID}, nil)
}

func (d *dex) chat(t *testing.T, message string) map[string]string {
	t.Helper()
	var reply map[string]string
	d.must(t, http.MethodPost, "/api/chat", map[string]string{"message": message}, &reply)
	return reply
}

func TestUserGetsAnswerFromAgent(t *testing.T) {
	// Given a running dex with a model
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")

	// When the user sends two messages in the web chat
	first := d.chat(t, "hello")
	second := d.chat(t, "again")

	// Then each message gets the agent's answer
	if first["status"] != "done" || first["answer"] != "echo: hello" {
		t.Fatalf("first reply = %v", first)
	}
	if second["status"] != "done" || second["answer"] != "echo: again" {
		t.Fatalf("second reply = %v", second)
	}
}

func TestUserChoosesModelInSettings(t *testing.T) {
	// Given a dex that starts for the first time, without settings
	d := startDex(t, t.TempDir(), "")

	// When the user sends a message
	before := d.chat(t, "hello")

	// Then the reply tells the user to choose a model
	if before["status"] != "unanswered" || !strings.Contains(before["reason"], "choose one in Settings") {
		t.Fatalf("reply without a model = %v", before)
	}

	// When the user picks a model from the model list and sends a message
	if got := d.models(t); !slices.Contains(got, "faux/echo") {
		t.Fatalf("models = %q", got)
	}
	d.setModel(t, "echo")
	after := d.chat(t, "hello")

	// Then the agent answers
	if after["answer"] != "echo: hello" {
		t.Fatalf("reply with a model = %v", after)
	}
}

func TestModelChangeAppliesToExistingChat(t *testing.T) {
	// Given a chat with the echo model
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	d.chat(t, "hi")

	// When the user changes the model in the settings and sends another message in the same chat
	d.setModel(t, "shout")
	reply := d.chat(t, "again")

	// Then the new model answers
	if reply["answer"] != "shout: again" {
		t.Fatalf("reply = %v", reply)
	}
}

func TestUserProviderKeyIsSavedAndNeverShown(t *testing.T) {
	const secret = "sk-ant-test-0123456789"
	dataDir := t.TempDir()

	// Given a dex where the user added the built-in Anthropic provider with an API key
	d := startDex(t, dataDir, "")
	d.must(t, http.MethodPut, "/api/settings/providers/anthropic", map[string]string{"api": "", "apiKey": secret}, nil)

	// When dex restarts and the user opens the settings
	d.stop()
	d = startDex(t, dataDir, "")
	_, raw := d.call(t, http.MethodGet, "/api/settings", nil)

	// Then the worker has the key from the settings, and the response does not hold the key
	if strings.Contains(raw, secret) {
		t.Fatal("GET /api/settings returned the API key")
	}
	if !strings.Contains(raw, `{"id":"anthropic","name":"Anthropic","source":"settings"}`) {
		t.Fatalf("the worker has no key for anthropic: %s", raw)
	}
	if !strings.Contains(raw, `"id":"anthropic","name":"Anthropic","api":"","baseUrl":"","keySet":true`) {
		t.Fatalf("anthropic is not in the added providers: %s", raw)
	}
}

// fakeModelsEndpoint serves the model list of an OpenAI-style (Bearer key, /models) or an
// Anthropic-style (x-api-key, /v1/models) API. Providers are a system boundary.
func fakeModelsEndpoint(t *testing.T, path, header, value, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Header.Get(header) != value {
			http.Error(w, "wrong path or key", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestUserAddsCustomProviders(t *testing.T) {
	// Given an OpenAI-compatible gateway and an Anthropic-compatible endpoint
	openai := fakeModelsEndpoint(t, "/v1/models", "Authorization", "Bearer k1", `{"data":[{"id":"gw-model"}]}`)
	anthropic := fakeModelsEndpoint(t, "/v1/models", "X-Api-Key", "k2",
		`{"data":[{"id":"claude-x","display_name":"Claude X"}]}`)
	d := startDex(t, t.TempDir(), "")

	// When the user adds three custom providers: two that list their models, and one with fixed
	// models and no key
	d.must(t, http.MethodPut, "/api/settings/providers/gw", map[string]any{
		"api": "openai-completions", "name": "Gateway", "baseUrl": openai + "/v1", "apiKey": "k1",
	}, nil)
	d.must(t, http.MethodPut, "/api/settings/providers/ant", map[string]any{
		"api": "anthropic-messages", "baseUrl": anthropic, "apiKey": "k2",
	}, nil)
	d.must(t, http.MethodPut, "/api/settings/providers/local", map[string]any{
		"api": "openai-responses", "baseUrl": "http://127.0.0.1:9/v1", "models": []string{"m1", " m2 ", "m1"},
	}, nil)

	// Then the model list has the models of all three, and the agent can use one
	want := []string{"gw/gw-model", "ant/claude-x", "local/m1", "local/m2"}
	if got := d.models(t); !containsAll(got, want) {
		t.Fatalf("models = %q, want %q in them", got, want)
	}
	d.must(t, http.MethodPut, "/api/settings/agent", map[string]string{"provider": "local", "modelId": "m2"}, nil)

	// When the user removes the gateway
	d.must(t, http.MethodDelete, "/api/settings/providers/gw", nil, nil)

	// Then its models are gone, and the others stay
	got := d.models(t)
	if slices.Contains(got, "gw/gw-model") || !containsAll(got, want[1:]) {
		t.Fatalf("models after removing gw = %q", got)
	}
}

func TestProviderWhoseModelListFailsIsSavedWithAnError(t *testing.T) {
	// Given an endpoint that refuses the key
	url := fakeModelsEndpoint(t, "/v1/models", "Authorization", "Bearer right", `{"data":[]}`)
	d := startDex(t, t.TempDir(), "")

	// When the user adds it with a wrong key
	status, raw := d.call(t, http.MethodPut, "/api/settings/providers/gw", map[string]any{
		"api": "openai-completions", "baseUrl": url + "/v1", "apiKey": "wrong",
	})

	// Then dex reports why, and keeps the provider so the user can fix the key
	if status != http.StatusInternalServerError || !strings.Contains(raw, "listing the models failed") ||
		!strings.Contains(raw, "HTTP 401") {
		t.Fatalf("PUT: %d %s", status, raw)
	}
	_, raw = d.call(t, http.MethodGet, "/api/settings", nil)
	if !strings.Contains(raw, `"id":"gw"`) {
		t.Fatalf("the provider is not saved: %s", raw)
	}
}

func (d *dex) models(t *testing.T) []string {
	t.Helper()
	var list struct {
		Models []struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		} `json:"models"`
	}
	d.must(t, http.MethodGet, "/api/models", nil, &list)
	var refs []string
	for _, m := range list.Models {
		refs = append(refs, m.Provider+"/"+m.ID)
	}
	return refs
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

func TestInvalidSettingsAreRefused(t *testing.T) {
	d := startDex(t, t.TempDir(), "")
	for _, c := range []struct {
		name, path string
		body       any
	}{
		{"unknown model", "/api/settings/agent", map[string]string{"provider": "faux", "modelId": "nope"}},
		{"unknown built-in provider", "/api/settings/providers/nope", map[string]string{"api": "", "apiKey": "k"}},
		{"unknown API", "/api/settings/providers/x", map[string]string{"api": "bogus", "baseUrl": "http://127.0.0.1:1"}},
		{"custom ID of a built-in provider", "/api/settings/providers/anthropic",
			map[string]string{"api": "anthropic-messages", "baseUrl": "http://127.0.0.1:1"}},
		{"custom ID with capitals", "/api/settings/providers/MyGW",
			map[string]string{"api": "openai-completions", "baseUrl": "http://127.0.0.1:1/v1"}},
		{"base URL with a password", "/api/settings/providers/gw",
			map[string]string{"api": "openai-completions", "baseUrl": "http://user:pw@127.0.0.1:1/v1"}},
		{"Telegram without allowed users", "/api/settings/telegram",
			map[string]any{"token": "123:abc", "allowedUsers": []int64{}}},
		{"bad Telegram token", "/api/settings/telegram", map[string]any{"token": "abc", "allowedUsers": []int64{1}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if status, raw := d.call(t, http.MethodPut, c.path, c.body); status != http.StatusBadRequest {
				t.Fatalf("status = %d %s", status, raw)
			}
		})
	}
}

func TestCrossSiteWriteIsRefused(t *testing.T) {
	// Given a running dex
	d := startDex(t, t.TempDir(), "")

	// When another web site in the user's browser posts a message to dex
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/chat",
		strings.NewReader(`{"message":"rm -rf"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Then dex refuses it
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestUserMessageLeftPendingIsAnsweredAfterRestart(t *testing.T) {
	dataDir := t.TempDir()

	// Given a chat that exists, and a message that control stored but the worker never
	// answered, as after a crash
	d := startDex(t, dataDir, "")
	d.setModel(t, "echo")
	d.chat(t, "hello")
	d.stop()
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	const pendingID = "0199e1c2-0000-7000-8000-000000000001"
	_, err = q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
		ID: pendingID, ConversationID: 1, Content: "are you there", CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// When dex starts again
	startDex(t, dataDir, "")

	// Then the worker answers the pending message
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var last sqlc.Submission
	for {
		last, err = q.GetSubmission(ctx, pendingID)
		if err != nil {
			t.Fatal(err)
		}
		if last.Status != "pending" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("submission still pending: %+v", last)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if last.Status != "done" || last.Answer.String != "echo: are you there" {
		t.Fatalf("submission = %+v", last)
	}
}
