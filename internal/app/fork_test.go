package app_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type forkInfo struct {
	sessionInfo
	Cwd      string `json:"cwd"`
	Settings struct {
		ModelID       string `json:"modelId"`
		ThinkingLevel string `json:"thinkingLevel"`
	} `json:"settings"`
	ForkedFrom *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		Main  bool   `json:"main"`
	} `json:"forkedFrom"`
}

// fork forks chatID at entryID and returns the new chat.
func (d *dex) fork(t *testing.T, chatID, entryID int64, before bool) forkInfo {
	t.Helper()
	var out struct {
		ChatID int64 `json:"chatId"`
	}
	d.must(t, http.MethodPost, chatPath(chatID, "fork"), map[string]any{"entryId": entryID, "before": before}, &out, http.StatusCreated)
	var c forkInfo
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", out.ChatID), nil, &c)
	return c
}

func findItem(t *testing.T, tr *transcript, kind, text string) item {
	t.Helper()
	i := slices.IndexFunc(tr.items, func(it item) bool { return it.Kind == kind && it.Text == text })
	if i < 0 {
		t.Fatalf("no %s item %q: %+v", kind, text, tr.items)
	}
	return tr.items[i]
}

func TestUserForksChat(t *testing.T) {
	t.Parallel()
	// Given a chat with its own model, thinking level and project, and two answers.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	src := d.newSession(t).ID
	project := t.TempDir()
	d.must(t, http.MethodPut, projectURL(src), map[string]string{"cwd": project}, nil)
	d.must(t, http.MethodPut, chatPath(src, "settings"), map[string]string{"provider": "faux", "modelId": "context", "thinkingLevel": "high"}, nil)
	w := d.watch(t, src)
	d.send(t, src, "one")
	w.until(t, "first answer", answered(`context: ["one"]`))
	d.send(t, src, "two")
	w.until(t, "second answer", answered(`context: ["one","two"]`))
	d.waitSettled(t, src)
	first := findItem(t, &w.tr, "assistant", `context: ["one"]`)
	second := findItem(t, &w.tr, "user", "two")
	opening := findItem(t, &w.tr, "user", "one")

	// When the user forks from the first answer.
	fork := d.fork(t, src, first.EntryID, false)

	// Then the fork is a new listed Chat with the source's settings, and it does not run.
	if fork.ForkedFrom == nil || fork.ForkedFrom.ID != src || fork.ForkedFrom.Main || fork.Cwd != project ||
		fork.Settings.ModelID != "context" || fork.Settings.ThinkingLevel != "high" || fork.Busy {
		t.Fatalf("fork = %+v", fork)
	}
	if !slices.ContainsFunc(d.chats(t), func(c chatInfo) bool { return c.ID == fork.ID }) {
		t.Fatal("the fork is not in the chat list")
	}
	if fresh := d.newSession(t).ID; fresh == fork.ID {
		t.Fatal("a new session reused the fork")
	}
	fw := d.watch(t, fork.ID)
	fw.until(t, "fork history", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && countKind(tr, "user") == 1 && last.Kind == "assistant" && last.Text == `context: ["one"]`
	})

	// When the user sends in the fork, then the model sees the copied history and not the source's later message.
	d.send(t, fork.ID, "three")
	fw.until(t, "fork answer", answered(`context: ["one","three"]`))
	// The source is unchanged.
	if slices.ContainsFunc(w.tr.items, func(it item) bool { return it.Text == "three" }) {
		t.Fatalf("the fork changed the source: %+v", w.tr.items)
	}

	// When the user edits the second message in a new chat, then that chat ends before the message.
	edit := d.fork(t, src, second.EntryID, true)
	d.send(t, edit.ID, "two again")
	d.watch(t, edit.ID).until(t, "edited answer", answered(`context: ["one","two again"]`))

	// When the user edits the first message in a new chat, then the chat is empty but keeps the settings.
	empty := d.fork(t, src, opening.EntryID, true)
	if empty.ForkedFrom == nil || empty.Cwd != project || empty.Settings.ModelID != "context" {
		t.Fatalf("empty fork = %+v", empty)
	}
	if !slices.ContainsFunc(d.chats(t), func(c chatInfo) bool { return c.ID == empty.ID }) {
		t.Fatal("the empty fork is not in the chat list")
	}
	d.send(t, empty.ID, "fresh")
	d.watch(t, empty.ID).until(t, "empty fork answer", answered(`context: ["fresh"]`))

	// A fork must start from an answer that is in the chat.
	for _, body := range []map[string]any{{"entryId": second.EntryID}, {"entryId": 999999}, {"entryId": 0}} {
		d.must(t, http.MethodPost, chatPath(src, "fork"), body, nil, http.StatusBadRequest)
	}
	d.must(t, http.MethodPost, chatPath(fork.ID, "fork"), map[string]any{"entryId": second.EntryID}, nil, http.StatusBadRequest)
}

func TestUserForksRunningChat(t *testing.T) {
	t.Parallel()
	// Given a chat with one answer that now runs a command which waits for a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	src := d.newSession(t).ID
	w := d.watch(t, src)
	d.send(t, src, "ready")
	w.until(t, "first answer", answered("echo: ready"))
	answer := findItem(t, &w.tr, "assistant", "echo: ready")
	d.setModel(t, "bash")
	d.send(t, src, taskGate(t, dir, "fork-busy"))
	waitTaskFile(t, filepath.Join(dir, "fork-busy.pid"))
	var call item
	w.until(t, "running tool", func(tr *transcript) bool {
		i := slices.IndexFunc(tr.items, func(it item) bool { return it.Kind == "tool" && it.EntryID > 0 })
		if i >= 0 {
			call = tr.items[i]
		}
		return tr.running && i >= 0
	})

	// When the user forks during the run, then the fork has only the committed answer and does not run.
	fork := d.fork(t, src, answer.EntryID, false)
	fw := d.watch(t, fork.ID)
	fw.until(t, "idle fork", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && len(tr.items) == 2 && last.Text == "echo: ready"
	})
	if fork.Busy {
		t.Fatalf("the fork is busy: %+v", fork)
	}
	// A call that waits for its result cannot start a fork.
	d.must(t, http.MethodPost, chatPath(src, "fork"), map[string]any{"entryId": call.EntryID}, nil, http.StatusBadRequest)

	// When the user stops the source, then a fork from the earlier answer has no stopped run.
	d.must(t, http.MethodPost, chatPath(src, "abort"), nil, nil)
	w.until(t, "stopped", func(tr *transcript) bool { return !tr.running && countKind(tr, "error") > 0 })
	after := d.fork(t, src, answer.EntryID, false)
	d.watch(t, after.ID).until(t, "fork after stop", func(tr *transcript) bool {
		return !tr.running && len(tr.items) == 2 && countKind(tr, "error") == 0
	})
	if err := os.WriteFile(filepath.Join(dir, "fork-busy.gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
