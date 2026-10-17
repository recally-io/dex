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

	"github.com/recally-io/dex/internal/db/sqlc"
)

// upload sends one file as POST /api/agents/default/uploads and returns the status and the body. A filename with
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/agents/default/uploads", &body)
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

func (d *dex) setThinking(t *testing.T, thinkingLevel string) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/agents/default",
		map[string]string{"name": "Dex", "provider": "faux", "modelId": "think", "thinkingLevel": thinkingLevel, "instructions": ""}, nil)
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
		ThinkingLevel string `json:"thinkingLevel"`
	}
	d.must(t, http.MethodGet, "/api/agents/default", nil, &s)
	if s.ThinkingLevel != "off" {
		t.Fatalf("default thinking level = %q", s.ThinkingLevel)
	}

	// When the user sets the level high
	d.setThinking(t, "high")

	// Then the settings keep it, and pi-ai sends it to the model
	d.must(t, http.MethodGet, "/api/agents/default", nil, &s)
	if s.ThinkingLevel != "high" {
		t.Fatalf("saved thinking level = %q", s.ThinkingLevel)
	}
	chatID := d.newChat(t, "hi")
	w := d.watch(t, chatID)
	w.until(t, "answer at high", answered("think high: hi"))

	// When the user lowers it, the same chat uses the new level from its next message
	d.setThinking(t, "low")
	d.send(t, chatID, "again")
	w.until(t, "answer at low", answered("think low: again"))

	// And an unknown level is refused
	status, raw := d.call(t, http.MethodPut, "/api/agents/default",
		map[string]string{"name": "Dex", "provider": "faux", "modelId": "think", "thinkingLevel": "huge", "instructions": ""})
	if status != http.StatusBadRequest {
		t.Fatalf("unknown level: %d %s", status, raw)
	}
}

func TestUserMainUsesAgentSettings(t *testing.T) {
	// Given the main timeline and the agent's model settings.
	d := startDex(t, t.TempDir(), "")
	d.setThinking(t, "high")
	id := d.mainChat(t)
	d.send(t, id, "first")
	w := d.watch(t, id)
	w.until(t, "agent settings", answered("think high: first"))

	// When the agent settings change, the next input uses them.
	d.setThinking(t, "low")
	d.send(t, id, "second")
	w.until(t, "new agent settings", answered("think low: second"))

	// Then main and Chat refuse per-session overrides.
	for _, chatID := range []int64{id, d.newSession(t).ID} {
		d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", chatID), chatSettings{Provider: "faux", ModelID: "echo"}, nil, http.StatusForbidden)
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
	created := d.newSession(t)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", created.ID), map[string]any{
		"id": uuid.NewV7().String(), "text": "look", "attachments": []string{pic.ID, notes.ID},
	}, nil, http.StatusAccepted)
	w := d.watch(t, created.ID)

	// Then the model gets the text and the paths, and the transcript shows the text and the files
	paths := "Attached files:\n- " + pic.Path + "\n- " + notes.Path
	w.until(t, "answer", answered("echo: look\n\n"+paths))
	user := w.tr.items[0]
	if user.Kind != "user" || user.Text != "look" || !slices.Equal(user.Attachments, []attachment{pic, notes}) {
		t.Fatalf("user item = %+v", user)
	}

	// When the user sends only a file in a new chat
	only := d.mustUpload(t, "report.pdf", []byte("%PDF-1.4\n"))
	created = d.newSession(t)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", created.ID), map[string]any{
		"id": uuid.NewV7().String(), "text": " ", "attachments": []string{only.ID},
	}, nil, http.StatusAccepted)

	// Then the chat is named after the file, and the model gets the path
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", created.ID), nil, &created)
	if created.Title != "report.pdf" {
		t.Fatalf("file-only title: %+v", created)
	}
	w2 := d.watch(t, created.ID)
	w2.until(t, "answer", answered("echo: Attached files:\n- "+only.Path))

	// And a reload shows the files with the message
	again := d.watch(t, created.ID)
	again.until(t, "snapshot", func(tr *transcript) bool { return tr.snapshots == 1 })
	if got := again.tr.items[len(again.tr.items)-2]; got.Text != "" || !slices.Equal(got.Attachments, []attachment{only}) {
		t.Fatalf("reloaded user item = %+v", got)
	}
}

// getUpload returns the status and the headers of GET /api/agents/default/uploads/<rawID>, and the body.
func (d *dex) getUpload(t *testing.T, rawID string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+"/api/agents/default/uploads/"+rawID, nil)
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
	chatID := d.newSession(t).ID
	messagePath := fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID)
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
		status, raw := d.call(t, http.MethodPost, messagePath, c.body)
		if status != http.StatusBadRequest || !strings.Contains(raw, `"error":`) {
			t.Fatalf("%s: %d %s", c.name, status, raw)
		}
	}
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("refused messages created chats: %+v", chats)
	}
	// 10 attachments are allowed
	d.must(t, http.MethodPost, messagePath, map[string]any{"id": uuid.NewV7().String(), "text": "ten", "attachments": ids[:10]},
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
	_, err = sqlc.New(conn).CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{Kind: "message", Origin: "web",
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
