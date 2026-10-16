package app_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/mcptest"
)

// These tests run the real embedded worker with pi-ai's faux provider: model faux/echo answers
// "echo: <message>", faux/shout answers "shout: <message>", faux/bash runs the message with the
// bash tool and answers "bash: <output>", and faux/think answers "think <thinking level>:
// <message>". They do not prove that a real provider works.

type dex struct {
	stopOnce sync.Once
	app      *app.App
	srv      *httptest.Server
}

// cacheDir is shared by all tests. Install skips a worker that is already in it; unpacking the
// worker for each start is most of the run time under -race.
var cacheDir string

func TestMain(m *testing.M) {
	// The tests start this binary as a stdio MCP server; see mcpServer.
	if dir := os.Getenv("DEX_TEST_MCP_DIR"); dir != "" {
		if err := (&mcptest.Server{Dir: dir, Prefix: os.Getenv("DEX_TEST_MCP_PREFIX"),
			Twice: os.Getenv("DEX_TEST_MCP_TWICE") != ""}).ServeStdio(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "dex-app-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cacheDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// startDex starts dex with its own data directory and serves its handler. telegramURL is the Bot
// API URL, or empty.
func startDex(t *testing.T, dataDir, telegramURL string) *dex {
	t.Helper()
	return startDexWith(t, app.Config{DataDir: dataDir, TelegramAPIURL: telegramURL})
}

// startDexWith starts dex with cfg, a cache directory, and the test worker environment.
func startDexWith(t *testing.T, cfg app.Config) *dex {
	t.Helper()
	return startDexHome(t, cfg, t.TempDir())
}

// startDexHome starts dex as startDexWith does, with home as the worker's HOME.
func startDexHome(t *testing.T, cfg app.Config, home string) *dex {
	t.Helper()
	cfg.CacheDir = cacheDir
	cfg.WorkerEnv = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "DEX_FAKE_MODEL=1"}
	a, err := app.Start(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	d := &dex{app: a, srv: httptest.NewServer(a.Handler())}
	t.Cleanup(d.stop)
	return d
}

func (d *dex) stop() {
	d.stopOnce.Do(func() {
		d.srv.CloseClientConnections()
		d.srv.Close()
		_ = d.app.Close()
	})
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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

// must sends a request that must get status want, and decodes the response into out when out is
// not nil.
func (d *dex) must(t *testing.T, method, path string, body, out any, want ...int) {
	t.Helper()
	status, raw := d.call(t, method, path, body)
	if status != cmpOr(want, http.StatusOK) {
		t.Fatalf("%s %s: %d %s", method, path, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			t.Fatal(err)
		}
	}
}

func cmpOr(want []int, def int) int {
	if len(want) > 0 {
		return want[0]
	}
	return def
}

func (d *dex) setModel(t *testing.T, modelID string) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/settings/agent",
		map[string]string{"name": "Dex", "provider": "faux", "modelId": modelID, "thinkingLevel": "off", "instructions": ""}, nil)
}

type chatInfo struct {
	ID      int64  `json:"id"`
	Channel string `json:"channel"`
	Title   string `json:"title"`
	Busy    bool   `json:"busy"`
}

// newChat opens a separate Chat session and sends its first message.
func (d *dex) newChat(t *testing.T, text string) int64 {
	t.Helper()
	c := d.newSession(t)
	d.send(t, c.ID, text)
	return c.ID
}

// send sends text to chat id and returns the message ID.
func (d *dex) send(t *testing.T, chatID int64, text string) string {
	t.Helper()
	id := uuid.NewV7().String()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/messages", chatID), map[string]string{"id": id, "text": text},
		nil, http.StatusAccepted)
	return id
}

func (d *dex) chats(t *testing.T) []chatInfo {
	t.Helper()
	var list struct {
		Chats []chatInfo `json:"chats"`
	}
	d.must(t, http.MethodGet, "/api/chats", nil, &list)
	return list.Chats
}

// item is a transcript item of the chat event stream.
type item struct {
	Kind   string `json:"kind"`
	TaskID string `json:"taskId"`
	Title  string `json:"title"`
	At     int64  `json:"at"`
	ID     string `json:"id"`
	Text   string `json:"text"`
	Queued bool   `json:"queued"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output"`
	Reason string `json:"reason"`
	// Attachments of a user item.
	Attachments []attachment `json:"attachments"`
}

type attachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Path     string `json:"path"`
}

// transcript applies chat events as the web UI does: a snapshot replaces all, an item replaces the
// item with its ID or is added, and an append adds text to an item.
type transcript struct {
	items     []item
	running   bool
	snapshots int
}

type chatEvent struct {
	Type    string `json:"type"`
	Items   []item `json:"items"`
	Item    item   `json:"item"`
	ID      string `json:"id"`
	Text    string `json:"text"`
	Running bool   `json:"running"`
}

func (tr *transcript) apply(ev chatEvent) {
	switch ev.Type {
	case "snapshot":
		tr.items, tr.running = ev.Items, ev.Running
		tr.snapshots++
	case "item":
		if i := slices.IndexFunc(tr.items, func(it item) bool { return it.ID == ev.Item.ID }); i >= 0 {
			tr.items[i] = ev.Item
		} else {
			tr.items = append(tr.items, ev.Item)
		}
	case "append":
		if i := slices.IndexFunc(tr.items, func(it item) bool { return it.ID == ev.ID }); i >= 0 {
			tr.items[i].Text += ev.Text
		}
	case "run":
		tr.running = ev.Running
	}
}

// watcher reads the event stream of one chat.
type watcher struct {
	events chan chatEvent
	cancel context.CancelFunc
	tr     transcript
}

func (d *dex) watch(t *testing.T, chatID int64) *watcher {
	t.Helper()
	return d.watchPath(t, fmt.Sprintf("/api/chats/%d/events", chatID))
}

func (d *dex) watchPath(t *testing.T, path string) *watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.srv.Client().Do(req) //nolint:bodyclose // the reader goroutine closes it
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("watch: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	w := &watcher{events: make(chan chatEvent, 1024), cancel: cancel}
	t.Cleanup(cancel)
	go func() {
		defer resp.Body.Close()
		defer close(w.events)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, 16<<20)
		var typ string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var ev chatEvent
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil || ev.Type != typ {
					t.Errorf("bad event %q %q: %v", typ, line, err)
					return
				}
				w.events <- ev
			}
		}
	}()
	return w
}

// until applies events until cond holds, and fails with the last transcript after 30 s.
func (w *watcher) until(t *testing.T, what string, cond func(*transcript) bool) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for !cond(&w.tr) {
		select {
		case ev, ok := <-w.events:
			if !ok {
				t.Fatalf("stream ended before %s; transcript: %+v", what, w.tr)
			}
			w.tr.apply(ev)
		case <-deadline:
			t.Fatalf("no %s after 30 s; transcript: %+v", what, w.tr)
		}
	}
}

// answered holds when the run is over and the last item is an answer with text.
func answered(text string) func(*transcript) bool {
	return func(tr *transcript) bool {
		n := len(tr.items)
		return !tr.running && n > 0 && tr.items[n-1].Kind == "assistant" && tr.items[n-1].Text == text
	}
}

func TestUserGetsStreamedAnswerFromAgent(t *testing.T) {
	// Given a running dex with a model, and a new chat
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	chatID := d.newChat(t, "hello")

	// When the user watches the chat
	w := d.watch(t, chatID)

	// Then the answer arrives, after the user's message
	w.until(t, "first answer", answered("echo: hello"))
	if first := w.tr.items[0]; first.Kind != "user" || first.Text != "hello" {
		t.Fatalf("items = %+v", w.tr.items)
	}

	// When the user sends another message
	id := d.send(t, chatID, "again")

	// Then it shows with the client's ID, and its answer arrives
	w.until(t, "second answer", answered("echo: again"))
	if !slices.ContainsFunc(w.tr.items, func(it item) bool { return it.ID == id && it.Kind == "user" && !it.Queued }) {
		t.Fatalf("no user item %s: %+v", id, w.tr.items)
	}
	// The chat list learns of the answer when control records it, after the stream shows it.
	eventually(t, "chat not busy", func() (bool, any) {
		got := d.chats(t)
		return len(got) == 1 && got[0].Title == "hello" && got[0].Channel == "web" && !got[0].Busy, got
	})
}

// answeredWith holds when the run is over and the last item is an answer that contains text.
func answeredWith(text string) func(*transcript) bool {
	return func(tr *transcript) bool {
		n := len(tr.items)
		return !tr.running && n > 0 && tr.items[n-1].Kind == "assistant" && strings.Contains(tr.items[n-1].Text, text)
	}
}

func TestAgentRemembersAcrossChats(t *testing.T) {
	// Given a new dex: the user's memory repo has the default MEMORY.md
	dataDir := t.TempDir()
	repo := filepath.Join(dataDir, "memory")
	d := startDex(t, dataDir, "")
	d.setModel(t, "memory")
	recall := d.newChat(t, "what do you know?")
	r := d.watch(t, recall)
	r.until(t, "default memory", answeredWith("## Index"))

	// When the agent saves an entry in another chat
	d.setModel(t, "bash")
	entry := "- The user drinks green tea [added: 2026-10-14]"
	write := d.newChat(t, fmt.Sprintf("printf '%%s\\n' '%s' >> %s && echo saved", entry, filepath.Join(repo, "MEMORY.md")))
	d.watch(t, write).until(t, "memory write", answered("bash: saved"))

	// Then the first chat knows the entry from its next message, without a restart
	d.setModel(t, "memory")
	d.send(t, recall, "and now?")
	r.until(t, "remembered entry", answeredWith(entry))

	// And a new chat knows it too
	d.watch(t, d.newChat(t, "hello")).until(t, "entry in new chat", answeredWith(entry))

	// And the change is in the repo's history
	eventually(t, "memory commit", func() (bool, any) {
		log := gitLog(t, dataDir)
		want := fmt.Sprintf("Update memory from dex:chat/%d", write)
		return slices.ContainsFunc(log, func(c string) bool {
			return strings.HasPrefix(c, want+"\n") && strings.Contains(c, "+"+entry)
		}), log
	})
}

// gitLog returns the commits of the memory repo in dataDir, oldest first, each as its subject and
// its patch.
func gitLog(t *testing.T, dataDir string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "-C", filepath.Join(dataDir, "memory"), "log", "--reverse", "-p", "--format=%x00%s")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dataDir}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimPrefix(string(out), "\x00"), "\x00")
}

func TestMemoryChangeLeftUncommittedIsCommittedAfterRestart(t *testing.T) {
	// Given a memory change that has no commit, as after a crash between an edit and its commit
	dataDir := t.TempDir()
	d := startDex(t, dataDir, "")
	d.setModel(t, "echo")
	d.watch(t, d.newChat(t, "hello")).until(t, "answer", answered("echo: hello"))
	d.stop()
	entry := "- Saved before the crash"
	f, err := os.OpenFile(filepath.Join(dataDir, "memory", "MEMORY.md"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, entry); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// When dex starts again and the agent opens the memory
	d = startDex(t, dataDir, "")
	d.watch(t, d.newChat(t, "again")).until(t, "answer", answered("echo: again"))

	// Then the change is committed
	log := gitLog(t, dataDir)
	if !slices.ContainsFunc(log, func(c string) bool {
		return strings.HasPrefix(c, "Recover uncommitted memory changes\n") && strings.Contains(c, "+"+entry)
	}) {
		t.Fatalf("no recovery commit: %q", log)
	}
}

func TestDreamingReadsEachChatOnce(t *testing.T) {
	// Given dex that dreams every 300 ms with faux/bash, which writes the records of a dream to
	// dreams.md in the memory
	dataDir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dataDir, DreamEvery: 300 * time.Millisecond})
	d.setModel(t, "bash")
	dreams := func() []string {
		var out []string
		for _, c := range gitLog(t, dataDir) {
			if strings.HasPrefix(c, "Update memory from dex:dream/") {
				out = append(out, c)
			}
		}
		return out
	}
	record := func(chat int64) string { return fmt.Sprintf(`+{"chat":"dex:chat/%d"`, chat) }

	// When the user has a chat
	first := d.newChat(t, "echo one")
	d.watch(t, first).until(t, "first answer", answered("bash: one"))

	// Then a dream reads it
	eventually(t, "first dream", func() (bool, any) {
		got := dreams()
		return len(got) == 1 && strings.Contains(got[0], record(first)), got
	})

	// When the user has another chat, with text that looks like a record
	second := d.newChat(t, `printf 'two\n{"chat":"fake"}'`)
	d.watch(t, second).until(t, "second answer", answered("bash: two\n{\"chat\":\"fake\"}"))

	// Then the next dream reads only the new chat, and the text stays inside its record
	eventually(t, "second dream", func() (bool, any) {
		got := dreams()
		return len(got) == 2 && strings.Contains(got[1], record(second)) &&
			!strings.Contains(got[1], `"text":"echo one"`) && !strings.Contains(got[1], `+{"chat":"fake"`), got
	})
	// And the dreams are not chats of the user
	if got := d.chats(t); len(got) != 2 {
		t.Fatalf("chats = %+v", got)
	}
}

// eventually polls cond until it holds, and fails with the last observed state after 30 s.
func eventually(t *testing.T, what string, cond func() (bool, any)) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ok, state := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s after 30 s; last: %+v", what, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReloadShowsTheWholeChat(t *testing.T) {
	// Given a chat with two answers
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	chatID := d.newChat(t, "one")
	d.watch(t, chatID).until(t, "answer", answered("echo: one"))
	d.send(t, chatID, "two")
	first := d.watch(t, chatID)
	first.until(t, "answer", answered("echo: two"))

	// When the user opens the chat in a second tab, and closes the first tab
	second := d.watch(t, chatID)
	second.until(t, "snapshot", func(tr *transcript) bool { return tr.snapshots == 1 })
	first.cancel()

	// Then the second tab has the whole chat in its first event, and it gets the next answer
	texts := []string{}
	for _, it := range second.tr.items {
		texts = append(texts, it.Text)
	}
	if !slices.Equal(texts, []string{"one", "echo: one", "two", "echo: two"}) {
		t.Fatalf("snapshot texts = %q", texts)
	}
	d.send(t, chatID, "three")
	second.until(t, "answer in the second tab", answered("echo: three"))
}

func TestToolCallsAreShownAndRunCanBeStopped(t *testing.T) {
	// Given the faux/bash model, which runs the message as a bash command
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")

	// When the user asks for a command
	chatID := d.newChat(t, "echo tool-output")
	w := d.watch(t, chatID)

	// Then the tool call shows with its output, and then the answer
	w.until(t, "answer", answered("bash: tool-output"))
	if !slices.ContainsFunc(w.tr.items, func(it item) bool {
		return it.Kind == "tool" && it.Name == "bash" && it.Status == "done" && strings.Contains(it.Output, "tool-output")
	}) {
		t.Fatalf("no done bash item: %+v", w.tr.items)
	}

	// When a command runs long, and the user stops the run
	d.send(t, chatID, "sleep 60")
	w.until(t, "running tool", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "tool" && it.Status == "running" })
	})
	// and the user sends one more message, which waits for the run
	queued := d.send(t, chatID, "echo later")
	w.until(t, "queued message", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == queued && it.Queued })
	})
	d.must(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/abort", chatID), nil, nil)

	// Then the queued message shows as stopped
	w.until(t, "withdrawn message", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == queued+":error" && it.Kind == "error" })
	})

	// And the run ends without an answer to the long command
	w.until(t, "stopped run", func(tr *transcript) bool {
		return !tr.running && !slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "tool" && it.Status == "running" })
	})
	if slices.ContainsFunc(w.tr.items, func(it item) bool {
		return it.Kind == "assistant" && strings.Contains(it.Text, "bash: ") && it.Text != "bash: tool-output"
	}) {
		t.Fatalf("the stopped command got an answer: %+v", w.tr.items)
	}
}

func TestUserMustChooseModelBeforeChatting(t *testing.T) {
	// Given a dex that starts for the first time, without settings
	d := startDex(t, t.TempDir(), "")

	// When the user sends input to a session without a model.
	c := d.newSession(t)
	status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/messages", c.ID), map[string]string{"id": uuid.NewV7().String(), "text": "hello"})

	// Then dex refuses it, tells the user to choose a model, and stores nothing
	if status != http.StatusConflict || !strings.Contains(raw, "choose one in Settings") {
		t.Fatalf("POST without a model: %d %s", status, raw)
	}
	if got := d.chats(t); len(got) != 0 {
		t.Fatalf("chats = %+v", got)
	}

	// When the user picks a model from the model list and starts a chat
	if got := d.models(t); !slices.Contains(got, "faux/echo") {
		t.Fatalf("models = %q", got)
	}
	d.setModel(t, "echo")
	chatID := d.newChat(t, "hello")

	// Then the agent answers
	d.watch(t, chatID).until(t, "answer", answered("echo: hello"))
}

func TestRetriedMessageIsStoredOnce(t *testing.T) {
	// Given a chat
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	first := map[string]string{"id": uuid.NewV7().String(), "text": "hi"}
	chatID := d.newSession(t).ID
	path := fmt.Sprintf("/api/chats/%d/messages", chatID)
	d.must(t, http.MethodPost, path, first, nil, http.StatusAccepted)

	// When the browser sends the same requests again, for example after a network error
	d.must(t, http.MethodPost, path, first, nil, http.StatusAccepted)
	again := map[string]string{"id": uuid.NewV7().String(), "text": "again"}
	d.must(t, http.MethodPost, path, again, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, path, again, nil, http.StatusAccepted)

	// Then dex has one chat with two messages and two answers
	w := d.watch(t, chatID)
	w.until(t, "answer", answered("echo: again"))
	if got := d.chats(t); len(got) != 1 {
		t.Fatalf("chats = %+v", got)
	}
	if len(w.tr.items) != 4 {
		t.Fatalf("items = %+v", w.tr.items)
	}
}

func TestModelChangeAppliesToExistingChat(t *testing.T) {
	// Given a chat with the echo model
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	chatID := d.newChat(t, "hi")
	w := d.watch(t, chatID)
	w.until(t, "answer", answered("echo: hi"))

	// When the user changes the model in the settings and sends another message in the same chat
	d.setModel(t, "shout")
	d.send(t, chatID, "again")

	// Then the new model answers
	w.until(t, "answer", answered("shout: again"))
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
	var s struct {
		Builtins []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"builtins"`
		Providers []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			KeySet     bool   `json:"keySet"`
			ModelCount int    `json:"modelCount"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(s.Builtins, func(b struct {
		ID     string `json:"id"`
		Source string `json:"source"`
	}) bool {
		return b.ID == "anthropic" && b.Source == "settings"
	}) {
		t.Fatalf("the worker has no key for anthropic: %s", raw)
	}
	if len(s.Providers) != 1 || s.Providers[0].Name != "Anthropic" || !s.Providers[0].KeySet || s.Providers[0].ModelCount == 0 {
		t.Fatalf("providers = %+v", s.Providers)
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
	var gw struct {
		ModelCount int `json:"modelCount"`
	}
	d.must(t, http.MethodPut, "/api/settings/providers/gw", map[string]any{
		"api": "openai-completions", "name": "Gateway", "baseUrl": openai + "/v1", "apiKey": "k1",
	}, &gw)
	d.must(t, http.MethodPut, "/api/settings/providers/ant", map[string]any{
		"api": "anthropic-messages", "baseUrl": anthropic, "apiKey": "k2",
	}, nil)
	d.must(t, http.MethodPut, "/api/settings/providers/local", map[string]any{
		"api": "openai-responses", "baseUrl": "http://127.0.0.1:9/v1", "models": []string{"m1", " m2 ", "m1"},
	}, nil)

	// Then the model list has the models of all three, and the agent can use one
	if gw.ModelCount != 1 {
		t.Fatalf("gw model count = %d", gw.ModelCount)
	}
	want := []string{"gw/gw-model", "ant/claude-x", "local/m1", "local/m2"}
	if got := d.models(t); !containsAll(got, want) {
		t.Fatalf("models = %q, want %q in them", got, want)
	}
	d.must(t, http.MethodPut, "/api/settings/agent", map[string]string{"name": "Dex", "provider": "local", "modelId": "m2", "thinkingLevel": "off", "instructions": ""}, nil)

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
	var saved struct {
		Error string `json:"error"`
	}
	d.must(t, http.MethodPut, "/api/settings/providers/gw", map[string]any{
		"api": "openai-completions", "baseUrl": url + "/v1", "apiKey": "wrong",
	}, &saved)

	// Then dex saves it, so the user can fix the key, and reports why it has no models, also later
	if !strings.Contains(saved.Error, "HTTP 401") {
		t.Fatalf("PUT error = %q", saved.Error)
	}
	_, raw := d.call(t, http.MethodGet, "/api/settings", nil)
	if !strings.Contains(raw, `"id":"gw"`) || !strings.Contains(raw, "HTTP 401") {
		t.Fatalf("settings: %s", raw)
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

func TestInvalidRequestsAreRefused(t *testing.T) {
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	for _, c := range []struct {
		name, method, path string
		body               any
		status             int
	}{
		{"unknown model", http.MethodPut, "/api/settings/agent", map[string]string{"name": "Dex", "provider": "faux", "modelId": "nope", "thinkingLevel": "off"}, 400},
		{"unknown built-in provider", http.MethodPut, "/api/settings/providers/nope", map[string]string{"api": "", "apiKey": "k"}, 400},
		{"unknown API", http.MethodPut, "/api/settings/providers/x", map[string]string{"api": "bogus", "baseUrl": "http://127.0.0.1:1"}, 400},
		{"custom ID of a built-in provider", http.MethodPut, "/api/settings/providers/anthropic",
			map[string]string{"api": "anthropic-messages", "baseUrl": "http://127.0.0.1:1"}, 400},
		{"custom ID with capitals", http.MethodPut, "/api/settings/providers/MyGW",
			map[string]string{"api": "openai-completions", "baseUrl": "http://127.0.0.1:1/v1"}, 400},
		{"base URL with a password", http.MethodPut, "/api/settings/providers/gw",
			map[string]string{"api": "openai-completions", "baseUrl": "http://user:pw@127.0.0.1:1/v1"}, 400},
		{"negative Telegram user ID", http.MethodPut, "/api/settings/telegram",
			map[string]any{"token": "123:abc", "allowedUsers": []int64{-1}}, 400},
		{"bad Telegram token", http.MethodPut, "/api/settings/telegram", map[string]any{"token": "abc", "allowedUsers": []int64{1}}, 400},
		{"message ID that is not a UUID", http.MethodPost, "/api/chats/999/messages", map[string]string{"id": "1", "text": "hi"}, 400},
		{"empty message", http.MethodPost, "/api/chats/999/messages", map[string]string{"id": uuid.NewV7().String(), "text": "  "}, 400},
		{"body that is not JSON", http.MethodPost, "/api/chats/999/messages", "hi", 400},
		{"message to a chat that does not exist", http.MethodPost, "/api/chats/999/messages",
			map[string]string{"id": uuid.NewV7().String(), "text": "hi"}, 404},
		{"events of a chat that does not exist", http.MethodGet, "/api/chats/999/events", nil, 404},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, raw := d.call(t, c.method, c.path, c.body)
			if status != c.status || !strings.Contains(raw, `"error":`) {
				t.Fatalf("status = %d %s", status, raw)
			}
		})
	}
}

func TestCrossSiteWriteIsRefused(t *testing.T) {
	// Given a running dex
	d := startDex(t, t.TempDir(), "")

	// When another web site in the user's browser posts a message to dex
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/chats",
		strings.NewReader(`{"id":"0199e1c2-0000-7000-8000-000000000001","text":"rm -rf"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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
	chatID := d.newChat(t, "hello")
	d.watch(t, chatID).until(t, "answer", answered("echo: hello"))
	d.stop()
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	pendingID := uuid.NewV7().String()
	_, err = q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{Kind: "message", Origin: "web",
		ID: pendingID, ConversationID: chatID, Content: "are you there", Attachments: "[]", CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// When dex starts again and the user opens the chat
	d = startDex(t, dataDir, "")
	w := d.watch(t, chatID)

	// Then the worker answers the pending message
	w.until(t, "answer", answered("echo: are you there"))
	eventually(t, "settled submission", func() (bool, any) {
		last, err := q.GetSubmission(t.Context(), pendingID)
		return err == nil && last.Status == "done" && last.Answer.String == "echo: are you there", last
	})
}
