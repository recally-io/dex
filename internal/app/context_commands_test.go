package app_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"
)

func sessionPath(sessionID int64, action string) string {
	return fmt.Sprintf("/api/bots/default/sessions/%d/%s", sessionID, action)
}

// compaction matches a compaction boundary with status; "" is a placed summary.
func compaction(status string) func(*transcript) bool {
	return func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool {
			return it.Kind == "boundary" && it.Boundary == "compaction" && it.Status == status
		})
	}
}

// waitSettled waits until dex has settled every message of the session; an answer can stream before.
func (d *dex) waitSettled(t *testing.T, sessionID int64) {
	t.Helper()
	eventually(t, "session settled", func() (bool, any) {
		var c sessionListItem
		d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d", sessionID), nil, &c)
		return !c.Busy, c
	})
}

func TestUserClearsContextWithHandoff(t *testing.T) {
	t.Parallel()
	// Given a session with one answered message.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "context")
	sessionID := d.newSession(t).ID
	d.send(t, sessionID, "old topic")
	w := d.watch(t, sessionID)
	w.until(t, "first answer", answered(`context: ["old topic"]`))
	d.waitSettled(t, sessionID)

	// When the user clears the context with a handoff.
	id := uuid.NewV7().String()
	d.must(t, http.MethodPost, sessionPath(sessionID, "clear"), map[string]string{"id": id, "handoff": "  keep the blue plan  "}, nil, http.StatusAccepted)
	// The same reset ID with another handoff is another request.
	d.must(t, http.MethodPost, sessionPath(sessionID, "clear"), map[string]string{"id": id, "handoff": "other"}, nil, http.StatusConflict)

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
	d.send(t, sessionID, "next")
	w.until(t, "answer after handoff", answered(`context: ["keep the blue plan","next"]`))
}

func TestUserCompactReplayRunsOnce(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	sessionID := d.newSession(t).ID
	w := d.watch(t, sessionID)
	d.send(t, sessionID, "short")
	w.until(t, "answer", answered("echo: short"))
	d.waitSettled(t, sessionID)
	id := uuid.NewV7().String()
	body := map[string]string{"id": id, "instructions": "keep notes"}
	var results [4]int
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i], _ = d.call(t, http.MethodPost, sessionPath(sessionID, "compact"), body) })
	}
	wg.Wait()
	for _, status := range results {
		if status != http.StatusAccepted {
			t.Fatalf("concurrent compact statuses = %v", results)
		}
	}
	w.until(t, "compaction result", compaction("nothing"))
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), body, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), map[string]string{"id": id, "instructions": "different"}, nil, http.StatusConflict)
	other := d.newSession(t).ID
	d.send(t, other, "another session")
	d.watch(t, other).until(t, "other answer", answered("echo: another session"))
	d.waitSettled(t, other)
	d.must(t, http.MethodPost, sessionPath(other, "compact"), body, nil, http.StatusConflict)
	d.send(t, sessionID, "after")
	w.until(t, "next answer", answered("echo: after"))
	count := 0
	for _, item := range w.tr.items {
		if item.Kind == "boundary" && item.Boundary == "compaction" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("compaction boundaries = %d", count)
	}
}

func TestUserCompactRetryAfterSynchronousFailure(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	sessionID := d.newSession(t).ID
	w := d.watch(t, sessionID)
	d.send(t, sessionID, "short")
	w.until(t, "answer", answered("echo: short"))
	d.waitSettled(t, sessionID)
	d.must(t, http.MethodPut, "/api/bots/default", map[string]string{"name": "Dex", "provider": "", "modelId": "", "thinkingLevel": "off"}, nil)
	body := map[string]string{"id": uuid.NewV7().String()}
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), body, nil, http.StatusConflict)
	d.setModel(t, "echo")
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), body, nil, http.StatusAccepted)
	w.until(t, "compaction after retry", compaction("nothing"))
}

func TestUserCompactsSession(t *testing.T) {
	t.Parallel()
	// Given a session that never ran.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	sessionID := d.newSession(t).ID
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), map[string]string{}, nil, http.StatusConflict)

	// When the user compacts a short session, then the transcript says there was nothing to compact.
	d.send(t, sessionID, "short")
	w := d.watch(t, sessionID)
	w.until(t, "short answer", answered("echo: short"))
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), nil, nil, http.StatusAccepted)
	w.until(t, "nothing to compact", compaction("nothing"))

	// Given a context above the 20000 tokens that compaction keeps.
	d.fillContext(t, w, sessionID)

	// When the user compacts it with instructions, then the boundary shows a bounded local view.
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), map[string]string{"instructions": "keep the words"}, nil, http.StatusAccepted)
	w.until(t, "summary placed", compaction(""))
	if !slices.ContainsFunc(w.tr.items, func(it item) bool {
		return it.Kind == "boundary" && it.Boundary == "compaction" && it.Status == "" &&
			strings.Contains(it.Text, "Compacted history") && strings.Contains(it.Text, "keep the words") &&
			strings.Contains(it.Text, "alpha") && len(it.Text) <= 4096
	}) {
		t.Fatal("compact did not place a bounded local view with the input and instructions")
	}
	if slices.ContainsFunc(w.tr.items, func(it item) bool { return it.Boundary == "compaction" && it.Status == "failed" }) {
		t.Fatalf("a compaction failed: %+v", w.tr.items)
	}

	// The full input remains in the session history.
	status, history := d.call(t, http.MethodGet, sessionPath(sessionID, "history?before=9007199254740991&limit=100"), nil)
	if status != http.StatusOK || !strings.Contains(history, strings.Repeat("alpha ", 200)) {
		t.Fatalf("full input is missing from history (HTTP %d)", status)
	}

	// When the next message runs, then the session still answers.
	d.setModel(t, "echo")
	d.send(t, sessionID, "after")
	w.until(t, "answer after compaction", answered("echo: after"))
}

func TestUserAutomaticCompactUsesLocalView(t *testing.T) {
	t.Parallel()
	// Given a model with a small context window.
	d := startDex(t, t.TempDir(), "")
	sessionID := d.newSession(t).ID
	w := d.watch(t, sessionID)
	d.setModel(t, "section-small")

	// When the session reaches the automatic compaction threshold.
	for range 5 {
		previous := lastItem(&w.tr).ID
		d.send(t, sessionID, strings.Repeat("history ", 3750))
		w.until(t, "answer", func(tr *transcript) bool {
			last := lastItem(tr)
			return !tr.running && last.Kind == "assistant" && last.ID != previous
		})
	}

	// Then automatic compaction also places a local view and the session continues.
	if !slices.ContainsFunc(w.tr.items, func(it item) bool {
		return it.Boundary == "compaction" && it.Status == "" && strings.HasPrefix(it.Text, "Compacted history")
	}) {
		t.Fatal("automatic compact did not place a local view")
	}
}

// fillContext sends messages until the context is above the 20000 tokens that compaction keeps.
// The section model answers briefly, because the faux model streams 200 tokens a second.
func (d *dex) fillContext(t *testing.T, w *watcher, sessionID int64) {
	t.Helper()
	d.setModel(t, "section")
	for _, word := range []string{"alpha", "bravo", "charlie", "delta"} {
		before := countKind(&w.tr, "assistant")
		d.send(t, sessionID, strings.Repeat(word+" ", 30000/(len(word)+1)))
		w.until(t, word+" answer", func(tr *transcript) bool { return !tr.running && countKind(tr, "assistant") > before })
	}
}

func TestUserCompactsDuringRun(t *testing.T) {
	t.Parallel()
	// Given a long session that runs a command which waits for a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	sessionID := d.newSession(t).ID
	w := d.watch(t, sessionID)
	d.fillContext(t, w, sessionID)
	d.setModel(t, "bash")
	gate := taskGate(t, dir, "compact-busy")
	d.send(t, sessionID, gate)
	waitTaskFile(t, filepath.Join(dir, "compact-busy.pid"))

	// When the user compacts while the command runs.
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), nil, nil, http.StatusAccepted)

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
	// Given a long session that compacts while a command waits for a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	sessionID := d.newSession(t).ID
	w := d.watch(t, sessionID)
	d.fillContext(t, w, sessionID)
	d.setModel(t, "bash")
	d.send(t, sessionID, taskGate(t, dir, "compact-stop"))
	waitTaskFile(t, filepath.Join(dir, "compact-stop.pid"))
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), nil, nil, http.StatusAccepted)
	w.until(t, "summary ready", compaction("queued"))

	// When the user stops the run, then the waiting summary is dropped and the transcript says so.
	d.must(t, http.MethodPost, sessionPath(sessionID, "abort"), nil, nil)
	w.until(t, "summary dropped", func(tr *transcript) bool {
		return !tr.running && !compaction("queued")(tr) && slices.ContainsFunc(tr.items, func(it item) bool {
			return it.Boundary == "compaction" && it.Status == "failed" && strings.HasPrefix(it.Reason, "Stopped before the summary was added")
		})
	})
	// Nothing waits any more, so maintenance can start once the stopped run has settled.
	eventually(t, "maintenance after stop", func() (bool, any) {
		status, body := d.call(t, http.MethodPost, "/api/maintenance", nil)
		return status == http.StatusOK, body
	})
	d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)

	// When the user sends the next message, then the session answers.
	d.setModel(t, "echo")
	d.send(t, sessionID, "after stop")
	w.until(t, "answer after stop", answered("echo: after stop"))
	if compaction("")(&w.tr) {
		t.Fatalf("a dropped summary was placed: %+v", w.tr.items)
	}
}

func TestUserClearsCompactedContext(t *testing.T) {
	t.Parallel()
	// Given a session with a placed compact view.
	d := startDex(t, t.TempDir(), "")
	sessionID := d.newSession(t).ID
	w := d.watch(t, sessionID)
	d.fillContext(t, w, sessionID)
	d.waitSettled(t, sessionID)
	d.must(t, http.MethodPost, sessionPath(sessionID, "compact"), nil, nil, http.StatusAccepted)
	w.until(t, "compacted", compaction(""))

	// When the user clears the session and sends another message.
	d.must(t, http.MethodPost, sessionPath(sessionID, "clear"), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusAccepted)
	d.setModel(t, "context")
	d.send(t, sessionID, "fresh context")

	// Then the model sees neither the compact view nor the kept turns.
	w.until(t, "fresh context only", answered(`context: ["fresh context"]`))
}
