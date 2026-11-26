package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"uuid"
)

func (d *dex) mainSession(t *testing.T) int64 {
	t.Helper()
	return d.mainInfo(t).ID
}

func (d *dex) mainInfo(t *testing.T) sessionInfo {
	t.Helper()
	var c sessionInfo
	d.must(t, http.MethodGet, "/api/agents/default/sessions/main", nil, &c)
	return c
}

func TestUserReturnsToMainAfterRestart(t *testing.T) {
	t.Parallel()
	// Given the main timeline with an answered message.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	id := d.mainSession(t)
	if again := d.mainSession(t); again != id {
		t.Fatalf("main changed: %d != %d", again, id)
	}
	d.send(t, id, "keep this timeline")
	w := d.watch(t, id)
	w.until(t, "main answer", answered("echo: keep this timeline"))
	before := w.tr.items
	d.stop()

	// When dex starts again, the same session and committed items remain.
	d = startDex(t, dir, "")
	if again := d.mainSession(t); again != id {
		t.Fatalf("main changed after restart: %d != %d", again, id)
	}
	w = d.watch(t, id)
	w.until(t, "restored answer", answered("echo: keep this timeline"))
	if !reflect.DeepEqual(before, w.tr.items) {
		t.Fatalf("transcript changed: before=%+v after=%+v", before, w.tr.items)
	}
}

func (d *dex) clearMain(t *testing.T, sessionID int64, id string) {
	t.Helper()
	eventually(t, "idle timeline accepts clear", func() (bool, any) {
		status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/default/sessions/%d/clear", sessionID), map[string]string{"id": id})
		return status == http.StatusAccepted, fmt.Sprintf("%d %s", status, raw)
	})
}

func TestUserClearsMainAndReadsEarlierHistory(t *testing.T) {
	t.Parallel()
	// Given an answered message in the main timeline.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "context")
	id := d.mainSession(t)
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
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
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
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/sessions/%d/history?before=9223372036854775807&limit=1", id), nil, &history)
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
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/sessions/%d/history?before=%d&limit=1", id, cursor), nil, &history)
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
	t.Logf("artifact: session=%d reset=%s stable_items=%d dividers=%d", id, resetID, len(all), dividers)
}

func TestUserCannotClearBusyMain(t *testing.T) {
	t.Parallel()
	// Given a main answer held in a real shell tool, followed by a queued input.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	id := d.mainSession(t)
	d.send(t, id, taskGate(t, dir, "clear-busy"))
	waitTaskFile(t, filepath.Join(dir, "clear-busy.pid"))
	queued := d.send(t, id, "printf queued")
	conn := taskResultDB(t, dir)
	resetID := uuid.NewV7().String()

	// When the owner clears, the server refuses without storing a command.
	status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/default/sessions/%d/clear", id), map[string]string{"id": resetID})
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
	t.Parallel()
	// Given a failed answer whose input and error appear in the transcript.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	id := d.mainSession(t)
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
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/sessions/%d/history?before=9007199254740991&limit=100", id), nil, &page)
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

func TestUserMainModelAppliesToWebAndChannelInput(t *testing.T) {
	t.Parallel()
	// Given Main on the agent model and a paired channel.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	id := d.mainSession(t)
	if got := taskTurn(t, d, id, "one"); got != "echo: one" {
		t.Fatal(got)
	}
	// When the user picks another model and thinking level for Main.
	url := fmt.Sprintf("/api/agents/default/sessions/%d/settings", id)
	d.must(t, http.MethodPut, url, map[string]string{"provider": "faux", "modelId": "think", "thinkingLevel": "high"}, nil)
	// Then web input and the next channel input both use them.
	if got := taskTurn(t, d, id, "two"); got != "think high: two" {
		t.Fatal(got)
	}
	f.message(1, 42, 7, "three")
	if got := f.next(t); got.Text != "think high: three" {
		t.Fatalf("channel answer: %+v", got)
	}
	// And the setting is part of Main's view.
	var view struct {
		Settings sessionSettings `json:"settings"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/sessions/main", nil, &view)
	if view.Settings.ModelID != "think" || view.Settings.ThinkingLevel != "high" {
		t.Fatalf("settings: %+v", view.Settings)
	}
}

func TestUserClearOrdersWithConcurrentInput(t *testing.T) {
	t.Parallel()
	// Given an idle timeline, then Clear and message admission start at the same time.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	id := d.mainSession(t)
	conn := taskResultDB(t, dir)
	accepted := 0
	for range 8 {
		resetID, inputID := uuid.NewV7().String(), uuid.NewV7().String()
		start := make(chan struct{})
		type response struct {
			status int
			raw    string
		}
		cleared, sent := make(chan response, 1), make(chan response, 1)
		go func() {
			<-start
			status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/default/sessions/%d/clear", id), map[string]string{"id": resetID})
			cleared <- response{status, raw}
		}()
		go func() {
			<-start
			status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/default/sessions/%d/messages", id), map[string]string{"id": inputID, "text": "concurrent input"})
			sent <- response{status, raw}
		}()
		close(start)
		clearReply, inputReply := <-cleared, <-sent
		if inputReply.status != http.StatusAccepted {
			t.Fatalf("send: %d %s", inputReply.status, inputReply.raw)
		}
		waitResultSubmission(t, conn, inputID, "done")
		// Then an accepted reset precedes the input; a refused reset has no stored command.
		var resetRow int64
		err := conn.QueryRowContext(t.Context(), "SELECT rowid FROM submissions WHERE id=?", resetID).Scan(&resetRow)
		switch clearReply.status {
		case http.StatusAccepted:
			if err != nil {
				t.Fatal(err)
			}
			var inputRow int64
			if err := conn.QueryRowContext(t.Context(), "SELECT rowid FROM submissions WHERE id=?", inputID).Scan(&inputRow); err != nil {
				t.Fatal(err)
			}
			if resetRow >= inputRow {
				t.Fatal("reset passed an admitted input")
			}
			waitResultSubmission(t, conn, resetID, "done")
			accepted++
		case http.StatusConflict:
			if err != sql.ErrNoRows {
				t.Fatalf("refused reset changed storage: %v", err)
			}
		default:
			t.Fatalf("clear: %d %s", clearReply.status, clearReply.raw)
		}
	}
	t.Logf("artifact: concurrent admissions=8 accepted=%d refused=%d", accepted, 8-accepted)
}

func TestUserClearsEmptyMainWithoutModel(t *testing.T) {
	t.Parallel()
	// Given a new timeline without a model, Clear does not need generation settings.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	id := d.mainSession(t)
	resetID := uuid.NewV7().String()
	d.clearMain(t, id, resetID)
	conn := taskResultDB(t, dir)
	waitResultSubmission(t, conn, resetID, "done")
	assertSubmissionOrigin(t, conn, resetID, "web")
	// Then the divider loads and a request ID collision cannot change the context.
	w := d.watch(t, id)
	w.until(t, "empty context divider", func(tr *transcript) bool {
		return len(tr.items) == 1 && tr.items[0].Kind == "boundary"
	})
	d.setModel(t, "echo")
	inputID := d.send(t, id, "keep this input")
	waitResultSubmission(t, conn, inputID, "done")
	status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/default/sessions/%d/clear", id), map[string]string{"id": inputID})
	if status != http.StatusConflict || !strings.Contains(raw, "another input") {
		t.Fatalf("request ID collision: %d %s", status, raw)
	}
}
