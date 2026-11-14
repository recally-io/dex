package app_test

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestUserSeesCurrentChildStateWithoutText(t *testing.T) {
	t.Parallel()
	// Given a child held in a real bash call, with no assistant text.
	dir := t.TempDir()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "spawn")
	parent := d.mainChat(t)
	pw := d.watch(t, parent)
	release := filepath.Join(dir, "first")
	nextRelease := filepath.Join(dir, "second")
	args, _ := json.Marshal(map[string]string{"title": "Current state", "provider": "faux", "modelId": "bash", "cwd": dir,
		"message": fmt.Sprintf("while [ ! -f %q ]; do sleep 0.05; done; printf first", release)})
	d.send(t, parent, string(args))
	pw.until(t, "child dispatch", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.Name == "session_new" && it.Status == "done" {
				return true
			}
		}
		return false
	})
	var ref struct {
		ID string `json:"id"`
	}
	for _, it := range pw.tr.items {
		if it.Name == "session_new" {
			if err := json.Unmarshal([]byte(it.Output), &ref); err != nil {
				t.Fatal(err)
			}
		}
	}
	child, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	pw.until(t, "parent finishes while child still runs", func(tr *transcript) bool {
		return !tr.running && tr.children[child] == "running" && len(tr.items) > 0 && tr.items[len(tr.items)-1].Kind == "assistant" && tr.items[len(tr.items)-1].EntryID != 0
	})
	// A fork can copy a session card, but that does not make the child belong to the fork.
	fork := d.fork(t, parent, pw.tr.items[len(pw.tr.items)-1].EntryID, false)
	fw := d.watch(t, fork.ID)
	fw.until(t, "fork snapshot is complete", func(tr *transcript) bool { return tr.children[fork.ID] == "idle" })
	if _, ok := fw.tr.children[child]; ok {
		t.Fatal("fork streamed another parent's child state")
	}
	fw.cancel()
	cw := d.watch(t, child)
	cw.until(t, "child panel sees the same state", func(tr *transcript) bool { return tr.children[child] == "running" })
	for _, it := range cw.tr.items {
		if it.Kind == "assistant" && it.Text != "" {
			t.Fatal("execution must not depend on text")
		}
	}
	// When another input is queued, the first input's settlement must not close the shared watch.
	second := fmt.Sprintf("while [ ! -f %q ]; do sleep 0.05; done; printf second", nextRelease)
	queued := d.send(t, child, second)
	cw.until(t, "second input is queued", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.ID == queued && it.Queued {
				return true
			}
		}
		return false
	})
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cw.until(t, "second input runs", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.ID == queued && !it.Queued && tr.running {
				return true
			}
		}
		return false
	})
	pw.until(t, "first result arrives while the child still runs", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.Kind == "task" && tr.children[child] == "running" {
				return true
			}
		}
		return false
	})
	pw.cancel()
	pw = d.watch(t, parent)
	pw.until(t, "shared watch still observes the second run", func(tr *transcript) bool { return tr.children[child] == "running" })
	// Then the second settlement reaches the parent as current idle state.
	if err := os.WriteFile(nextRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	pw.until(t, "child becomes idle", func(tr *transcript) bool { return tr.children[child] == "idle" })
}
