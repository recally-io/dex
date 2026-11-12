package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"
)

func sendMode(t *testing.T, d *dex, chatID int64, id, text, mode string, want int) {
	t.Helper()
	body := map[string]string{"id": id, "text": text}
	if mode != "" {
		body["mode"] = mode
	}
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID), body, nil, want)
}

func queuedSteer(id string) func(*transcript) bool {
	return func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.ID == id && it.Queued && it.Steer {
				return true
			}
		}
		return false
	}
}

func assistantTexts(tr *transcript) []string {
	var out []string
	for _, it := range tr.items {
		if it.Kind == "assistant" {
			out = append(out, it.Text)
		}
	}
	return out
}

func TestUserMessageReplayRejectsChanges(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	first := d.newSession(t).ID
	id := uuid.NewV7().String()
	sendMode(t, d, first, id, "hello", "", http.StatusAccepted)
	sendMode(t, d, first, id, "hello", "followUp", http.StatusAccepted)
	sendMode(t, d, first, id, "changed", "", http.StatusConflict)
	sendMode(t, d, first, id, "hello", "steer", http.StatusConflict)
	second := d.newSession(t).ID
	sendMode(t, d, second, id, "hello", "", http.StatusConflict)

	status, raw := d.upload(t, "file", "note.txt", []byte("note"))
	if status != http.StatusCreated {
		t.Fatalf("upload: %d %s", status, raw)
	}
	var file attachment
	if err := json.Unmarshal([]byte(raw), &file); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/agents/default/chats/%d/messages", second)
	fileID := uuid.NewV7().String()
	body := map[string]any{"id": fileID, "text": "file", "attachments": []string{file.ID}}
	d.must(t, http.MethodPost, path, body, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, path, body, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, path, map[string]any{"id": fileID, "text": "file"}, nil, http.StatusConflict)
}

func TestUserSteersRunningMainWithoutWaitingForReply(t *testing.T) {
	t.Parallel()
	// Given Main runs a gated command.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	conn := taskResultDB(t, dir)
	mainID := d.mainChat(t)
	first := newGatedRun(t, "first")
	firstID := d.send(t, mainID, first.command)
	first.waitStarted(t)

	// When the user steers the run, then the message waits as a steer item.
	steerID := uuid.NewV7().String()
	sendMode(t, d, mainID, steerID, "echo steered", "steer", http.StatusAccepted)
	w := d.watch(t, mainID)
	w.until(t, "steer item queued", queuedSteer(steerID))

	// When the gated tool step ends, then the same run reads the steer and gives one answer.
	first.open()
	w.until(t, "steered answer", answered("bash: steered"))
	for _, text := range assistantTexts(&w.tr) {
		if text == "bash: finished" {
			t.Fatalf("the run answered before the steer joined: %q", assistantTexts(&w.tr))
		}
	}
	for _, id := range []string{firstID, steerID} {
		eventually(t, "receipt done", func() (bool, any) {
			status, reason, _ := receiptState(t, conn, id)
			return status == "done", status + " " + reason
		})
	}
}

func TestUserRejectMessageWhileBusyNeverRuns(t *testing.T) {
	t.Parallel()
	// Given a chat that runs a gated command.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	conn := taskResultDB(t, dir)
	chatID := d.newSession(t).ID
	run := newGatedRun(t, "run")
	d.send(t, chatID, run.command)
	run.waitStarted(t)

	// When a client sends a reject message twice with one ID, then both calls answer 409.
	ran := filepath.Join(t.TempDir(), "rejected-ran")
	id := uuid.NewV7().String()
	sendMode(t, d, chatID, id, fmt.Sprintf("touch '%s'; echo rejected", ran), "reject", http.StatusConflict)
	sendMode(t, d, chatID, id, fmt.Sprintf("touch '%s'; echo rejected", ran), "reject", http.StatusConflict)
	// An unknown mode is refused before dex stores the message.
	sendMode(t, d, chatID, uuid.NewV7().String(), "echo bad", "later", http.StatusBadRequest)

	// When the busy run ends, then the rejected message never runs.
	run.open()
	w := d.watch(t, chatID)
	w.until(t, "first answer", answered("bash: finished"))
	if status, reason, _ := receiptState(t, conn, id); status != "unanswered" || reason != "busy" {
		t.Fatalf("receipt: %s %s", status, reason)
	}
	// When the chat is idle, then a reject message runs.
	sendMode(t, d, chatID, uuid.NewV7().String(), "echo idle", "reject", http.StatusAccepted)
	w.until(t, "idle reject answer", answered("bash: idle"))
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the rejected message ran")
	}
}

func TestUserSteerCanUseTaskTools(t *testing.T) {
	t.Parallel()
	// Given a chat whose run waits in a gated bash step.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	chatID := d.newSession(t).ID
	run := newGatedRun(t, "run")
	args, err := json.Marshal(map[string]string{"command": run.command})
	if err != nil {
		t.Fatal(err)
	}
	d.send(t, chatID, "bash "+string(args))
	run.waitStarted(t)

	// When the user steers the run to list tasks.
	steerID := uuid.NewV7().String()
	sendMode(t, d, chatID, steerID, "session_list {}", "steer", http.StatusAccepted)
	w := d.watch(t, chatID)
	w.until(t, "steer item queued", queuedSteer(steerID))
	run.open()

	// Then the task tool works in the run that now has two inputs.
	w.until(t, "task tool answer", func(tr *transcript) bool {
		n := len(tr.items)
		return !tr.running && n > 0 && tr.items[n-1].Kind == "assistant" && strings.HasPrefix(tr.items[n-1].Text, "tool")
	})
	if last := w.tr.items[len(w.tr.items)-1].Text; !strings.HasPrefix(last, "tool: ") {
		t.Fatalf("task tool failed: %s", last)
	}
}
