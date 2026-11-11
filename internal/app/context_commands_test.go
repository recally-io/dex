package app_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"uuid"
)

func chatPath(chatID int64, action string) string {
	return fmt.Sprintf("/api/agents/default/chats/%d/%s", chatID, action)
}

func boundary(kind, status string) func(*transcript) bool {
	return func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool {
			return it.Kind == "boundary" && it.Boundary == kind && it.Status == status
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
	w.until(t, "nothing to compact", boundary("compaction", "nothing"))

	// Given a context above the 20000 tokens that compaction keeps. The section model answers
	// briefly, because the faux model streams 200 tokens a second.
	d.setModel(t, "section")
	for _, word := range []string{"alpha", "bravo", "charlie", "delta"} {
		before := countKind(&w.tr, "assistant")
		d.send(t, chatID, strings.Repeat(word+" ", 30000/(len(word)+1)))
		w.until(t, word+" answer", func(tr *transcript) bool { return !tr.running && countKind(tr, "assistant") > before })
	}

	// When the user compacts it with instructions, then a pending boundary becomes a summary.
	d.must(t, http.MethodPost, chatPath(chatID, "compact"), map[string]string{"instructions": "keep the words"}, nil, http.StatusAccepted)
	w.until(t, "summary placed", func(tr *transcript) bool {
		return !boundary("compaction", "pending")(tr) && slices.ContainsFunc(tr.items, func(it item) bool {
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
