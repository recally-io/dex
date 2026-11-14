package app_test

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type executionTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type sessionExecution struct {
	ChatID  int64           `json:"chatId"`
	AgentID string          `json:"agentId"`
	Phase   string          `json:"phase"`
	Queued  int             `json:"queued"`
	Turns   []executionTurn `json:"turns"`
}

func sessionTurn(tr *transcript, id, status string) bool {
	for _, session := range tr.sessions {
		for _, turn := range session.Turns {
			if turn.ID == id && turn.Status == status {
				return true
			}
		}
	}
	return false
}

func TestUserSeesChildExecutionBeforeTextAndAfterReconnect(t *testing.T) {
	t.Parallel()
	// Given a parent stream and a child whose real bash tool waits for a release file.
	dir := t.TempDir()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "spawn")
	parent := d.mainChat(t)
	pw := d.watch(t, parent)
	release := filepath.Join(dir, "release")
	args, _ := json.Marshal(map[string]string{"title": "Execution status", "provider": "faux", "modelId": "bash", "cwd": dir,
		"message": fmt.Sprintf("while [ ! -f %q ]; do sleep 0.05; done; printf finished", release)})
	d.send(t, parent, string(args))
	pw.until(t, "child dispatch", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.Name == "session_new" && it.Status == "done" {
				return true
			}
		}
		return false
	})
	var child struct {
		ID     string `json:"id"`
		TurnID string `json:"turnId"`
	}
	for _, it := range pw.tr.items {
		if it.Name == "session_new" {
			if err := json.Unmarshal([]byte(it.Output), &child); err != nil {
				t.Fatal(err)
			}
		}
	}
	if child.TurnID == "" {
		t.Fatal("the dispatch must identify its own submission, not infer completion from child text")
	}
	// When the child runs without an assistant text, the parent receives its native tool phase.
	pw.until(t, "parent receives child tool execution without history polling", func(tr *transcript) bool {
		for _, session := range tr.sessions {
			if fmt.Sprint(session.ChatID) == child.ID && session.Phase == "tool" && sessionTurn(tr, child.TurnID, "running") {
				return true
			}
		}
		return false
	})
	childPath := "/api/agents/default/chats/" + child.ID
	cw := d.watchPath(t, childPath+"/events")
	cw.until(t, "child has the same tool state before text", func(tr *transcript) bool {
		for _, session := range tr.sessions {
			if fmt.Sprint(session.ChatID) == child.ID && session.Phase == "tool" {
				return true
			}
		}
		return false
	})
	for _, it := range cw.tr.items {
		if it.Kind == "assistant" && it.Text != "" {
			t.Fatal("the held child must not need a text reply to show execution")
		}
	}
	// A queued input must not change the running submission's phase or outcome.
	queued := d.send(t, mustChatID(t, child.ID), "printf second")
	cw.until(t, "queued and active input coexist", func(tr *transcript) bool {
		return sessionTurn(tr, queued, "queued") && sessionTurn(tr, child.TurnID, "running")
	})
	cw.cancel()
	cw = d.watchPath(t, childPath+"/events")
	cw.until(t, "reconnect restores current native state", func(tr *transcript) bool {
		return sessionTurn(tr, queued, "queued") && sessionTurn(tr, child.TurnID, "running")
	})
	// Then completion comes from the first submission even while a later turn exists.
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	pw.until(t, "parent receives the dispatch outcome", func(tr *transcript) bool { return sessionTurn(tr, child.TurnID, "done") })
	cw.until(t, "queued input completes", func(tr *transcript) bool { return sessionTurn(tr, queued, "done") })
	pw.cancel()
	pw = d.watch(t, parent)
	pw.until(t, "parent reconnect restores completed submission", func(tr *transcript) bool { return sessionTurn(tr, child.TurnID, "done") })
	// Reuse of the child must not change the old card to running.
	id := d.send(t, mustChatID(t, child.ID), "printf reused")
	cw.until(t, "reused child completes", func(tr *transcript) bool { return sessionTurn(tr, id, "done") })
	pw.until(t, "old dispatch remains complete", func(tr *transcript) bool { return sessionTurn(tr, child.TurnID, "done") })
}

func mustChatID(t *testing.T, id string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(id, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUserSeesChildFailureWithoutAReply(t *testing.T) {
	t.Parallel()
	// Given a child whose faux provider rejects invalid tool JSON before it can reply.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "spawn")
	parent := d.mainChat(t)
	w := d.watch(t, parent)
	args, _ := json.Marshal(map[string]string{"title": "Failed child", "provider": "faux", "modelId": "tool", "message": "read not-json"})
	d.send(t, parent, string(args))
	// When native execution fails, the parent sees the failed submission, not a missing preview.
	w.until(t, "child failure reaches the parent", func(tr *transcript) bool {
		for _, session := range tr.sessions {
			if session.ChatID != parent {
				for _, turn := range session.Turns {
					if turn.Status == "failed" && turn.Reason != "" {
						return true
					}
				}
			}
		}
		return false
	})
	// Then a reconnect returns the same outcome.
	w.cancel()
	w = d.watch(t, parent)
	w.until(t, "failure survives reconnect", func(tr *transcript) bool {
		for _, session := range tr.sessions {
			for _, turn := range session.Turns {
				if turn.Status == "failed" {
					return true
				}
			}
		}
		return false
	})
}
