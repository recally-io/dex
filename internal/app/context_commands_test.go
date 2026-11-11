package app_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"uuid"
)

func chatPath(chatID int64, action string) string {
	return fmt.Sprintf("/api/agents/default/chats/%d/%s", chatID, action)
}

// compaction matches a compaction boundary with status; "" is a placed summary.
func compaction(status string) func(*transcript) bool {
	return func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool {
			return it.Kind == "boundary" && it.Boundary == "compaction" && it.Status == status
		})
	}
}

// waitSettled waits until dex has settled every message of the chat; an answer can stream before.
func (d *dex) waitSettled(t *testing.T, chatID int64) {
	t.Helper()
	eventually(t, "chat settled", func() (bool, any) {
		var c chatInfo
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", chatID), nil, &c)
		return !c.Busy, c
	})
}

func TestUserClearsContextWithHandoff(t *testing.T) {
	t.Parallel()
	// Given a chat with one answered message.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "context")
	chatID := d.newSession(t).ID
	d.send(t, chatID, "old topic")
	w := d.watch(t, chatID)
	w.until(t, "first answer", answered(`context: ["old topic"]`))
	d.waitSettled(t, chatID)

	// When the user clears the context with a handoff.
	id := uuid.NewV7().String()
	d.must(t, http.MethodPost, chatPath(chatID, "clear"), map[string]string{"id": id, "handoff": "  keep the blue plan  "}, nil, http.StatusAccepted)
	// The same reset ID with another handoff is another request.
	d.must(t, http.MethodPost, chatPath(chatID, "clear"), map[string]string{"id": id, "handoff": "other"}, nil, http.StatusBadRequest)

	// Then the reset boundary shows the handoff and no run starts.
	w.until(t, "reset with handoff", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "boundary" && last.Boundary == "reset" && last.Text == "keep the blue plan"
	})
	// The reset hides the old context, so any answer here comes from a run that the handoff started.
	if answers := countKind(&w.tr, "assistant"); answers != 0 {
		t.Fatalf("the handoff started a run: %+v", w.tr.items)
	}

	// When the user sends the next message, then the model reads the handoff and not the old topic.
	d.send(t, chatID, "next")
	w.until(t, "answer after handoff", answered(`context: ["keep the blue plan","next"]`))
}

func TestUserCompactsChat(t *testing.T) {
	t.Parallel()
	// Given a chat that never ran.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	chatID := d.newSession(t).ID
	d.must(t, http.MethodPost, chatPath(chatID, "compact"), map[string]string{}, nil, http.StatusConflict)

	// When the user compacts a short chat, then the transcript says there was nothing to compact.
	d.send(t, chatID, "short")
	w := d.watch(t, chatID)
	w.until(t, "short answer", answered("echo: short"))
	d.must(t, http.MethodPost, chatPath(chatID, "compact"), nil, nil, http.StatusAccepted)
	w.until(t, "nothing to compact", compaction("nothing"))

	// Given a context above the 20000 tokens that compaction keeps.
	d.fillContext(t, w, chatID)

	// When the user compacts it with instructions, then a pending boundary becomes a summary.
	d.must(t, http.MethodPost, chatPath(chatID, "compact"), map[string]string{"instructions": "keep the words"}, nil, http.StatusAccepted)
	w.until(t, "summary placed", func(tr *transcript) bool {
		return !compaction("pending")(tr) && slices.ContainsFunc(tr.items, func(it item) bool {
			return it.Kind == "boundary" && it.Boundary == "compaction" && it.Status == "" && it.Text != ""
		})
	})
	if slices.ContainsFunc(w.tr.items, func(it item) bool { return it.Boundary == "compaction" && it.Status == "failed" }) {
		t.Fatalf("a compaction failed: %+v", w.tr.items)
	}

	// When the next message runs, then the chat still answers.
	d.setModel(t, "echo")
	d.send(t, chatID, "after")
	w.until(t, "answer after compaction", answered("echo: after"))
}

// fillContext sends messages until the context is above the 20000 tokens that compaction keeps.
// The section model answers briefly, because the faux model streams 200 tokens a second.
func (d *dex) fillContext(t *testing.T, w *watcher, chatID int64) {
	t.Helper()
	d.setModel(t, "section")
	for _, word := range []string{"alpha", "bravo", "charlie", "delta"} {
		before := countKind(&w.tr, "assistant")
		d.send(t, chatID, strings.Repeat(word+" ", 30000/(len(word)+1)))
		w.until(t, word+" answer", func(tr *transcript) bool { return !tr.running && countKind(tr, "assistant") > before })
	}
}

func TestUserCompactsDuringRun(t *testing.T) {
	t.Parallel()
	// Given a long chat that runs a command which waits for a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	chatID := d.newSession(t).ID
	w := d.watch(t, chatID)
	d.fillContext(t, w, chatID)
	d.setModel(t, "bash")
	gate := taskGate(t, dir, "compact-busy")
	d.send(t, chatID, gate)
	waitTaskFile(t, filepath.Join(dir, "compact-busy.pid"))

	// When the user compacts while the command runs. The summarizer uses the chat model.
	d.setModel(t, "section")
	d.must(t, http.MethodPost, chatPath(chatID, "compact"), nil, nil, http.StatusAccepted)

	// Then the transcript shows the compaction while the run goes on. The summary is quick, so it
	// may already wait for the next boundary.
	w.until(t, "compaction while running", func(tr *transcript) bool {
		return tr.running && (compaction("pending")(tr) || compaction("queued")(tr))
	})

	// When the command ends, then the summary is placed and the run answers once.
	before := countKind(&w.tr, "assistant")
	if err := os.WriteFile(filepath.Join(dir, "compact-busy.gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.until(t, "summary and answer", func(tr *transcript) bool {
		return !tr.running && !compaction("queued")(tr) && compaction("")(tr)
	})
	if slices.ContainsFunc(w.tr.items, func(it item) bool { return it.Boundary == "compaction" && it.Status == "failed" }) {
		t.Fatalf("the compaction failed: %+v", w.tr.items)
	}
	if got := countKind(&w.tr, "assistant") - before; got > 1 {
		t.Fatalf("the run answered %d times: %+v", got, w.tr.items)
	}
}

func TestUserStopsRunAfterCompacting(t *testing.T) {
	t.Parallel()
	// Given a long chat that compacts while a command waits for a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	chatID := d.newSession(t).ID
	w := d.watch(t, chatID)
	d.fillContext(t, w, chatID)
	d.setModel(t, "bash")
	d.send(t, chatID, taskGate(t, dir, "compact-stop"))
	waitTaskFile(t, filepath.Join(dir, "compact-stop.pid"))
	d.setModel(t, "section")
	d.must(t, http.MethodPost, chatPath(chatID, "compact"), nil, nil, http.StatusAccepted)
	w.until(t, "summary ready", compaction("queued"))

	// When the user stops the run, then the summary still waits and is not shown as in progress.
	d.must(t, http.MethodPost, chatPath(chatID, "abort"), nil, nil)
	w.until(t, "stopped with summary ready", func(tr *transcript) bool {
		return !tr.running && compaction("queued")(tr) && !compaction("pending")(tr)
	})

	// When the user sends the next message, then the summary is placed before it.
	d.setModel(t, "echo")
	d.send(t, chatID, "after stop")
	w.until(t, "answer after summary", answered("echo: after stop"))
	if !compaction("")(&w.tr) || compaction("queued")(&w.tr) {
		t.Fatalf("the summary was not placed: %+v", w.tr.items)
	}
}
