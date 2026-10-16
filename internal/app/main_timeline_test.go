package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func (d *dex) mainChat(t *testing.T) int64 {
	t.Helper()
	var c chatInfo
	d.must(t, http.MethodGet, "/api/chats/main", nil, &c)
	return c.ID
}

func TestUserReturnsToMainAfterRestart(t *testing.T) {
	// Given the main timeline with an answered message.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	id := d.mainChat(t)
	if again := d.mainChat(t); again != id {
		t.Fatalf("main changed: %d != %d", again, id)
	}
	d.send(t, id, "keep this timeline")
	w := d.watch(t, id)
	w.until(t, "main answer", answered("echo: keep this timeline"))
	before := w.tr.items
	d.stop()

	// When dex starts again, the same chat and committed items remain.
	d = startDex(t, dir, "")
	if again := d.mainChat(t); again != id {
		t.Fatalf("main changed after restart: %d != %d", again, id)
	}
	w = d.watch(t, id)
	w.until(t, "restored answer", answered("echo: keep this timeline"))
	if !reflect.DeepEqual(before, w.tr.items) {
		t.Fatalf("transcript changed: before=%+v after=%+v", before, w.tr.items)
	}
}

func (d *dex) clearMain(t *testing.T, chatID int64, id string) {
	t.Helper()
	eventually(t, "idle timeline accepts clear", func() (bool, any) {
		status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/clear", chatID), map[string]string{"id": id})
		return status == http.StatusAccepted, fmt.Sprintf("%d %s", status, raw)
	})
}

func TestUserClearsMainAndReadsEarlierHistory(t *testing.T) {
	// Given an answered message in the main timeline.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "context")
	id := d.mainChat(t)
	d.send(t, id, "old context")
	w := d.watch(t, id)
	w.until(t, "old answer", answered(`context: ["old context"]`))
	before := slices.Clone(w.tr.items)
	resetID := uuid.NewV7().String()

	// When the owner clears context and sends another message.
	d.clearMain(t, id, resetID)
	w.until(t, "context divider", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "boundary" })
	})
	d.send(t, id, "new context")
	w.until(t, "answer without old context", answered(`context: ["new context"]`))
	after := slices.Clone(w.tr.items)
	d.stop()

	// When control replays a reset whose worker write already committed before a crash.
	conn, err := sql.Open("sqlite", filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET status='pending', settled_at=NULL WHERE id=?", resetID); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	d = startDex(t, dir, "")
	conn = taskResultDB(t, dir)
	waitResultSubmission(t, conn, resetID, "done")
	d.clearMain(t, id, resetID)
	fresh := d.watch(t, id)
	fresh.until(t, "fresh snapshot", func(tr *transcript) bool { return tr.snapshots > 0 && !tr.running })
	if slices.ContainsFunc(fresh.tr.items, func(it item) bool { return it.Text == "old context" }) {
		t.Fatalf("snapshot contains old context: %+v", fresh.tr.items)
	}
	var history struct {
		Items      []item `json:"items"`
		NextBefore *int64 `json:"nextBefore"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/history?before=9223372036854775807&limit=1", id), nil, &history)
	all := map[string]item{}
	for _, it := range fresh.tr.items {
		all[it.ID] = it
	}
	for {
		for _, it := range history.Items {
			all[it.ID] = it
		}
		if history.NextBefore == nil {
			break
		}
		cursor := *history.NextBefore
		history.NextBefore = nil
		d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/history?before=%d&limit=1", id, cursor), nil, &history)
		if history.NextBefore != nil && *history.NextBefore >= cursor {
			t.Fatal("history cursor did not advance")
		}
	}
	for _, it := range append(before, after...) {
		if got, ok := all[it.ID]; !ok || got.Text != it.Text {
			t.Fatalf("lost stable item: %+v; all=%+v", it, all)
		}
	}
	dividers := 0
	for _, it := range all {
		if it.Kind == "boundary" {
			dividers++
		}
	}
	if dividers != 1 {
		t.Fatalf("replay created %d dividers", dividers)
	}
	// A duplicate reset did not cut the new input from the model context.
	d.send(t, id, "still here")
	d.watch(t, id).until(t, "context after replay", answered(`context: ["new context","still here"]`))
	t.Logf("artifact: chat=%d reset=%s stable_items=%d dividers=%d", id, resetID, len(all), dividers)
}

func TestUserCannotClearBusyMain(t *testing.T) {
	// Given a main answer held in a real shell tool, followed by a queued input.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	id := d.mainChat(t)
	d.send(t, id, taskGate(t, dir, "clear-busy"))
	waitTaskFile(t, filepath.Join(dir, "clear-busy.pid"))
	queued := d.send(t, id, "printf queued")
	conn := taskResultDB(t, dir)
	resetID := uuid.NewV7().String()

	// When the owner clears, the server refuses without storing a command.
	status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/clear", id), map[string]string{"id": resetID})
	if status != http.StatusConflict {
		t.Fatalf("busy clear: %d %s", status, raw)
	}
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id=?", resetID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("refused reset was stored")
	}
	releaseResultGate(t, dir, "clear-busy")
	waitResultSubmission(t, conn, queued, "done")
	w := d.watch(t, id)
	w.until(t, "queued answer", answered("bash: queued"))
	if slices.ContainsFunc(w.tr.items, func(it item) bool { return it.Kind == "boundary" }) {
		t.Fatal("refused reset wrote a divider")
	}
}

func TestUserReadsFailedInputAcrossClear(t *testing.T) {
	// Given a failed answer whose input and error appear in the transcript.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	id := d.mainChat(t)
	input := d.send(t, id, "invalid JSON")
	conn := taskResultDB(t, dir)
	waitResultSubmission(t, conn, input, "unanswered")
	before := d.watch(t, id)
	before.until(t, "failed answer", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "error" })
	})
	// When a fresh snapshot hides that context, history restores the same error once.
	d.clearMain(t, id, uuid.NewV7().String())
	fresh := d.watch(t, id)
	fresh.until(t, "cleared snapshot", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "boundary" })
	})
	var page struct {
		Items []item `json:"items"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/history?before=9007199254740991&limit=100", id), nil, &page)
	all := map[string]item{}
	for _, it := range append(fresh.tr.items, page.Items...) {
		all[it.ID] = it
	}
	errors := 0
	for _, it := range all {
		if it.Kind == "error" {
			errors++
		}
	}
	if errors != 1 {
		t.Fatalf("failed input has %d errors; snapshot=%+v history=%+v", errors, fresh.tr.items, page.Items)
	}
	for _, it := range before.tr.items {
		if _, ok := all[it.ID]; !ok {
			t.Fatalf("failed item ID changed: %+v", it)
		}
	}
	t.Logf("artifact: failed input=%s snapshot=%+v history=%+v", input, fresh.tr.items, page.Items)
}

// An automation input is a delivery barrier without adding a second web conversation.
func (d *dex) automationBarrier(t *testing.T, text string) int64 {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(d.dataDir(t), "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	id := uuid.NewV7().String()
	c, err := q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "default", ChannelKey: "automation:barrier:" + id, Title: "Barrier", ModelProvider: "faux", ModelID: "echo", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: id, ConversationID: c.ID, Origin: "automation", Kind: "message", Content: text, Attachments: "null", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	return c.ID
}
