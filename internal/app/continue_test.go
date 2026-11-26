package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"uuid"
)

func continuePath(sessionID int64) string {
	return fmt.Sprintf("/api/agents/default/sessions/%d/continue", sessionID)
}

func lastItem(tr *transcript) item {
	if len(tr.items) == 0 {
		return item{}
	}
	return tr.items[len(tr.items)-1]
}

func countKind(tr *transcript, kind string) int {
	n := 0
	for _, it := range tr.items {
		if it.Kind == kind {
			n++
		}
	}
	return n
}

func TestUserRetriesStoppedRunWithoutSendingTheMessageAgain(t *testing.T) {
	t.Parallel()
	// Given a session whose run the user stopped in a tool step.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	sessionID := d.newSession(t).ID
	// A session that never ran has nothing to continue.
	d.must(t, http.MethodPost, continuePath(sessionID), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusConflict)
	run := newGatedRun(t, "run")
	d.send(t, sessionID, run.command)
	run.waitStarted(t)
	w := d.watch(t, sessionID)
	// A continue cannot start while the run is active.
	d.must(t, http.MethodPost, continuePath(sessionID), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusConflict)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/sessions/%d/abort", sessionID), nil, nil)
	w.until(t, "stopped card", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "error" && last.Failure == "aborted"
	})

	// When the user retries, then the same run goes on from the stopped tool step.
	id := uuid.NewV7().String()
	d.must(t, http.MethodPost, continuePath(sessionID), map[string]string{"id": id}, nil, http.StatusAccepted)
	w.until(t, "continued answer", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "assistant" && strings.HasPrefix(last.Text, "bash: ")
	})
	if users := countKind(&w.tr, "user"); users != 1 {
		t.Fatalf("the message was sent again: %d user items", users)
	}
	if countKind(&w.tr, "error") != 0 {
		t.Fatalf("the stopped card stays after the answer: %+v", w.tr.items)
	}

	// When the client repeats the same continue, then nothing runs again.
	d.must(t, http.MethodPost, continuePath(sessionID), map[string]string{"id": id}, nil, http.StatusAccepted)
	// When the user continues a complete answer, then the session says there is nothing to continue.
	d.must(t, http.MethodPost, continuePath(sessionID), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusAccepted)
	w.until(t, "nothing to continue", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "error" && last.Failure == "nothing_to_answer"
	})
	if answers := countKind(&w.tr, "assistant"); answers != 1 {
		t.Fatalf("answers = %d, want 1: %+v", answers, w.tr.items)
	}
}

func TestUserRetriesFailedReplyWithAnotherModel(t *testing.T) {
	t.Parallel()
	// Given a reply that failed with a model error.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "spawn")
	sessionID := d.newSession(t).ID
	d.send(t, sessionID, "not json")
	w := d.watch(t, sessionID)
	w.until(t, "failed card", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "error" && last.Failure == "model_error"
	})

	// When the user picks another model and retries, then the run answers the same message once.
	d.setModel(t, "echo")
	d.must(t, http.MethodPost, continuePath(sessionID), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusAccepted)
	w.until(t, "continued answer", answered("echo: not json"))
	if users := countKind(&w.tr, "user"); users != 1 {
		t.Fatalf("the message was sent again: %d user items", users)
	}
}
