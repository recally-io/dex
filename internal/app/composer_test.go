package app_test

import (
	"bytes"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/vaayne/dex/internal/db/sqlc"
)

// upload sends one file as POST /api/uploads and returns the status and the body. A filename with
// a path is sent as it is, as a client that is not a browser can do.
func (d *dex) upload(t *testing.T, field, filename string, content []byte) (int, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/uploads", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
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

func (d *dex) mustUpload(t *testing.T, filename string, content []byte) attachment {
	t.Helper()
	status, raw := d.upload(t, "file", filename, content)
	if status != http.StatusCreated {
		t.Fatalf("upload %s: %d %s", filename, status, raw)
	}
	var a attachment
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func pngImage(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type chatSettings struct {
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

type chatWithSettings struct {
	ID       int64        `json:"id"`
	Title    string       `json:"title"`
	Settings chatSettings `json:"settings"`
}

func (d *dex) setAgent(t *testing.T, modelID, thinkingLevel string) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/settings/agent",
		map[string]string{"provider": "faux", "modelId": modelID, "thinkingLevel": thinkingLevel, "instructions": ""}, nil)
}

func TestUserSetsAgentThinkingLevel(t *testing.T) {
	// Given dex with the reasoning model faux/think
	d := startDex(t, t.TempDir(), "")

	// Then the models carry pi-ai's reasoning, input and supported levels
	var listed struct {
		Models []struct {
			Provider       string   `json:"provider"`
			ID             string   `json:"id"`
			Reasoning      bool     `json:"reasoning"`
			Input          []string `json:"input"`
			ThinkingLevels []string `json:"thinkingLevels"`
		} `json:"models"`
	}
	d.must(t, http.MethodGet, "/api/models", nil, &listed)
	byID := map[string]int{}
	for i, m := range listed.Models {
		byID[m.Provider+"/"+m.ID] = i
	}
	think, echo := listed.Models[byID["faux/think"]], listed.Models[byID["faux/echo"]]
	if !think.Reasoning || !slices.Contains(think.ThinkingLevels, "high") || !slices.Contains(think.Input, "text") {
		t.Fatalf("faux/think = %+v", think)
	}
	if echo.Reasoning || !slices.Equal(echo.ThinkingLevels, []string{"off"}) {
		t.Fatalf("faux/echo = %+v", echo)
	}

	// And a new agent's thinking level is off, as pi-durable runs a conversation without one
	var s struct {
		Agent struct {
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"agent"`
	}
	d.must(t, http.MethodGet, "/api/settings", nil, &s)
	if s.Agent.ThinkingLevel != "off" {
		t.Fatalf("default thinking level = %q", s.Agent.ThinkingLevel)
	}

	// When the user sets the level high
	d.setAgent(t, "think", "high")

	// Then the settings keep it, and pi-ai sends it to the model
	d.must(t, http.MethodGet, "/api/settings", nil, &s)
	if s.Agent.ThinkingLevel != "high" {
		t.Fatalf("saved thinking level = %q", s.Agent.ThinkingLevel)
	}
	chatID := d.newChat(t, "hi")
	w := d.watch(t, chatID)
	w.until(t, "answer at high", answered("think high: hi"))

	// When the user lowers it, the same chat uses the new level from its next message
	d.setAgent(t, "think", "low")
	d.send(t, chatID, "again")
	w.until(t, "answer at low", answered("think low: again"))

	// And an unknown level is refused
	status, raw := d.call(t, http.MethodPut, "/api/settings/agent",
		map[string]string{"provider": "faux", "modelId": "think", "thinkingLevel": "huge", "instructions": ""})
	if status != http.StatusBadRequest {
		t.Fatalf("unknown level: %d %s", status, raw)
	}
}

func TestChatOverridesAgentSettings(t *testing.T) {
	// Given an agent with faux/echo
	d := startDex(t, t.TempDir(), "")
	d.setAgent(t, "echo", "off")

	// When the user starts chat A with faux/think at high, and chat B with the defaults
	var created struct {
		Chat chatWithSettings `json:"chat"`
	}
	d.must(t, http.MethodPost, "/api/chats", map[string]any{
		"id": uuid.NewV7().String(), "text": "a1",
		"settings": chatSettings{Provider: "faux", ModelID: "think", ThinkingLevel: "high"},
	}, &created, http.StatusAccepted)
	a := created.Chat.ID
	b := d.newChat(t, "b1")

	// Then each chat runs with its own settings
	if created.Chat.Settings != (chatSettings{Provider: "faux", ModelID: "think", ThinkingLevel: "high"}) {
		t.Fatalf("chat A settings = %+v", created.Chat.Settings)
	}
	wa, wb := d.watch(t, a), d.watch(t, b)
	wa.until(t, "chat A answer", answered("think high: a1"))
	wb.until(t, "chat B answer", answered("echo: b1"))

	// When the user changes the agent's defaults
	d.setAgent(t, "think", "medium")

	// Then chat B follows them, and chat A keeps its own
	d.send(t, b, "b2")
	wb.until(t, "chat B answer with the new default", answered("think medium: b2"))
	d.send(t, a, "a2")
	wa.until(t, "chat A answer with its override", answered("think high: a2"))

	// When the user keeps only a thinking level in chat A
	var saved chatWithSettings
	d.must(t, http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", a), chatSettings{ThinkingLevel: "low"}, &saved)
	if saved.Settings != (chatSettings{ThinkingLevel: "low"}) {
		t.Fatalf("saved = %+v", saved.Settings)
	}
	d.send(t, a, "a3")
	wa.until(t, "chat A answer with the default model", answered("think low: a3"))

	// When the user clears the override, chat A follows the defaults again
	d.must(t, http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", a), chatSettings{}, nil)
	var got chatWithSettings
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d", a), nil, &got)
	if got.Settings != (chatSettings{}) {
		t.Fatalf("cleared = %+v", got.Settings)
	}
	d.send(t, a, "a4")
	wa.until(t, "chat A answer with the defaults", answered("think medium: a4"))

	// And invalid settings are refused, also for a new chat, which is then not created
	before := len(d.chats(t))
	for _, c := range []struct {
		name, method, path string
		body               any
		status             int
	}{
		{"model without provider", http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", a), chatSettings{ModelID: "think"}, 400},
		{"unknown model", http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", a), chatSettings{Provider: "faux", ModelID: "nope"}, 400},
		{"unknown level", http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", a), chatSettings{ThinkingLevel: "huge"}, 400},
		{"chat that does not exist", http.MethodPut, "/api/chats/999/settings", chatSettings{}, 404},
		{"new chat with an unknown model", http.MethodPost, "/api/chats", map[string]any{
			"id": uuid.NewV7().String(), "text": "x", "settings": chatSettings{Provider: "faux", ModelID: "nope"},
		}, 400},
	} {
		status, raw := d.call(t, c.method, c.path, c.body)
		if status != c.status || !strings.Contains(raw, `"error":`) {
			t.Fatalf("%s: %d %s", c.name, status, raw)
		}
	}
	if after := len(d.chats(t)); after != before {
		t.Fatalf("chats = %d, want %d", after, before)
	}
}

func TestUserSendsFilesToTheAgent(t *testing.T) {
	// Given dex with a model
	dataDir := t.TempDir()
	d := startDex(t, dataDir, "")
	d.setModel(t, "echo")

	// When the user uploads an image and a text file
	img := pngImage(t)
	pic := d.mustUpload(t, "screen shot.png", img)
	notes := d.mustUpload(t, "笔记.txt", []byte("remember the milk\n"))

	// Then each is in the agent workspace under uploads/<id>/<name>
	if pic.Name != "screen shot.png" || pic.Size != int64(len(img)) || pic.MimeType != "image/png" ||
		pic.Path != "uploads/"+pic.ID+"/screen shot.png" {
		t.Fatalf("image upload = %+v", pic)
	}
	if notes.MimeType != "text/plain; charset=utf-8" || notes.Path != "uploads/"+notes.ID+"/笔记.txt" {
		t.Fatalf("text upload = %+v", notes)
	}
	stored, err := os.ReadFile(filepath.Join(dataDir, "agents", "default", "workspace", notes.Path))
	if err != nil || string(stored) != "remember the milk\n" {
		t.Fatalf("stored file = %q %v", stored, err)
	}

	// When the user sends a message with both
	var created struct {
		Chat chatWithSettings `json:"chat"`
	}
	d.must(t, http.MethodPost, "/api/chats", map[string]any{
		"id": uuid.NewV7().String(), "text": "look", "attachments": []string{pic.ID, notes.ID},
	}, &created, http.StatusAccepted)
	w := d.watch(t, created.Chat.ID)

	// Then the model gets the text and the paths, and the transcript shows the text and the files
	paths := "Attached files:\n- " + pic.Path + "\n- " + notes.Path
	w.until(t, "answer", answered("echo: look\n\n"+paths))
	user := w.tr.items[0]
	if user.Kind != "user" || user.Text != "look" || !slices.Equal(user.Attachments, []attachment{pic, notes}) {
		t.Fatalf("user item = %+v", user)
	}

	// When the user sends only a file in a new chat
	only := d.mustUpload(t, "report.pdf", []byte("%PDF-1.4\n"))
	d.must(t, http.MethodPost, "/api/chats", map[string]any{
		"id": uuid.NewV7().String(), "text": " ", "attachments": []string{only.ID},
	}, &created, http.StatusAccepted)

	// Then the chat is named after the file, and the model gets the path
	if created.Chat.Title != "report.pdf" {
		t.Fatalf("title = %q", created.Chat.Title)
	}
	w2 := d.watch(t, created.Chat.ID)
	w2.until(t, "answer", answered("echo: Attached files:\n- "+only.Path))

	// And a reload shows the files with the message
	again := d.watch(t, created.Chat.ID)
	again.until(t, "snapshot", func(tr *transcript) bool { return tr.snapshots == 1 })
	if got := again.tr.items[0]; got.Text != "" || !slices.Equal(got.Attachments, []attachment{only}) {
		t.Fatalf("reloaded user item = %+v", got)
	}
}

// getUpload returns the status and the headers of GET /api/uploads/<rawID>, and the body.
func (d *dex) getUpload(t *testing.T, rawID string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+"/api/uploads/"+rawID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

func TestUploadIsServedSafely(t *testing.T) {
	d := startDex(t, t.TempDir(), "")
	img := pngImage(t)
	pic := d.mustUpload(t, "a.png", img)
	page := d.mustUpload(t, "page.html", []byte("<html><script>alert(1)</script></html>"))

	for _, c := range []struct {
		name, id, contentType, disposition string
		body                               []byte
	}{
		{"image inline", pic.ID, "image/png", `inline; filename=a.png`, img},
		{"HTML only as a download", page.ID, "application/octet-stream", `attachment; filename=page.html`,
			[]byte("<html><script>alert(1)</script></html>")},
	} {
		status, h, body := d.getUpload(t, c.id)
		if status != http.StatusOK || h.Get("Content-Type") != c.contentType || h.Get("Content-Disposition") != c.disposition ||
			h.Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(body, c.body) {
			t.Fatalf("%s: %d %v %q", c.name, status, h, body)
		}
	}

	// The agent's tools can put symlinks in the workspace; an upload never leads out of it.
	uploads := filepath.Join(d.dataDir(t), "agents", "default", "workspace", "uploads")
	dirLink, fileLink := uuid.NewV7().String(), uuid.NewV7().String()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(uploads, dirLink)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(uploads, fileLink), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(uploads, fileLink, "a.txt")); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{uuid.NewV7().String(), "..%2F..%2Fcontrol.sqlite", "not-an-id", dirLink, fileLink} {
		if status, _, body := d.getUpload(t, id); status != http.StatusNotFound {
			t.Fatalf("GET %s: %d %s", id, status, body)
		}
	}

	// A symlink in the place of the uploads directory leads neither reads nor writes out of it.
	outsideID := uuid.NewV7().String()
	if err := os.Mkdir(filepath.Join(outside, outsideID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, outsideID, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(uploads); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, uploads); err != nil {
		t.Fatal(err)
	}
	if status, _, body := d.getUpload(t, outsideID); status != http.StatusNotFound {
		t.Fatalf("GET through a replaced uploads directory: %d %s", status, body)
	}
	if status, body := d.upload(t, "file", "a.txt", []byte("a")); status == http.StatusCreated {
		t.Fatalf("POST through a replaced uploads directory: %d %s", status, body)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 2 {
		t.Fatalf("outside directory changed: %v %v", entries, err)
	}
}

func TestInvalidUploadsAndAttachmentsAreRefused(t *testing.T) {
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	workspace := filepath.Join(d.dataDir(t), "agents", "default", "workspace")

	// Uploads: too large, a name with a path, no name, no file
	for _, c := range []struct {
		name, field, filename string
		size                  int
		status                int
	}{
		{"file of 20 MB and 1 byte", "file", "big.bin", 20<<20 + 1, http.StatusRequestEntityTooLarge},
		{"name with a parent directory", "file", "../escape.txt", 1, http.StatusBadRequest},
		{"name with a directory", "file", "dir/a.txt", 1, http.StatusBadRequest},
		{"name with a Windows path", "file", `..\a.txt`, 1, http.StatusBadRequest},
		{"no name", "file", "", 1, http.StatusBadRequest},
		{"no part named file", "other", "a.txt", 1, http.StatusBadRequest},
	} {
		status, raw := d.upload(t, c.field, c.filename, bytes.Repeat([]byte("x"), c.size))
		if status != c.status || !strings.Contains(raw, `"error":`) {
			t.Fatalf("%s: %d %s", c.name, status, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("a refused upload left a file: %v", err)
	}
	// A file of exactly 20 MB is stored
	d.mustUpload(t, "max.bin", bytes.Repeat([]byte("x"), 20<<20))

	// Messages: too many, unknown, repeated, or no attachments
	ids := make([]string, 11)
	for i := range ids {
		ids[i] = d.mustUpload(t, fmt.Sprintf("f%d.txt", i), []byte("x")).ID
	}
	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"11 attachments", map[string]any{"text": "hi", "attachments": ids}},
		{"unknown upload", map[string]any{"text": "hi", "attachments": []string{uuid.NewV7().String()}}},
		{"upload ID with a path", map[string]any{"text": "hi", "attachments": []string{"../../control.sqlite"}}},
		{"same upload twice", map[string]any{"text": "hi", "attachments": []string{ids[0], ids[0]}}},
		{"no text and no attachment", map[string]any{"text": " ", "attachments": []string{}}},
	} {
		c.body["id"] = uuid.NewV7().String()
		status, raw := d.call(t, http.MethodPost, "/api/chats", c.body)
		if status != http.StatusBadRequest || !strings.Contains(raw, `"error":`) {
			t.Fatalf("%s: %d %s", c.name, status, raw)
		}
	}
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("refused messages created chats: %+v", chats)
	}
	// 10 attachments are allowed
	d.must(t, http.MethodPost, "/api/chats", map[string]any{"id": uuid.NewV7().String(), "text": "ten", "attachments": ids[:10]},
		nil, http.StatusAccepted)
}

func (d *dex) dataDir(t *testing.T) string {
	t.Helper()
	var health struct {
		DataDir string `json:"dataDir"`
	}
	d.must(t, http.MethodGet, "/api/health", nil, &health)
	return health.DataDir
}

func TestPendingMessageWithAttachmentsIsReplayedAfterRestart(t *testing.T) {
	dataDir := t.TempDir()

	// Given a chat, and a message with a file that control stored but the worker never got, as
	// after a crash
	d := startDex(t, dataDir, "")
	d.setModel(t, "echo")
	notes := d.mustUpload(t, "notes.txt", []byte("x"))
	chatID := d.newChat(t, "hello")
	d.watch(t, chatID).until(t, "answer", answered("echo: hello"))
	d.stop()
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	attachments, err := json.Marshal([]attachment{notes})
	if err != nil {
		t.Fatal(err)
	}
	pendingID := uuid.NewV7().String()
	_, err = sqlc.New(conn).CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
		ID: pendingID, ConversationID: chatID, Content: "read it", Attachments: string(attachments),
		CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// When dex starts again
	d = startDex(t, dataDir, "")
	w := d.watch(t, chatID)

	// Then the worker gets the same message with the file
	w.until(t, "answer", answered("echo: read it\n\nAttached files:\n- "+notes.Path))
	i := slices.IndexFunc(w.tr.items, func(it item) bool { return it.ID == pendingID })
	if i < 0 || w.tr.items[i].Text != "read it" || !slices.Equal(w.tr.items[i].Attachments, []attachment{notes}) {
		t.Fatalf("items = %+v", w.tr.items)
	}
}
