package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"uuid"
)

func TestUserKeepsOriginThroughBackgroundFollowUps(t *testing.T) {
	for _, origin := range []string{"web", "telegram:7"} {
		t.Run(origin, func(t *testing.T) {
			// Given a main or channel input that requests two successive background answers.
			f := newFakeTelegram(t)
			dir := t.TempDir()
			d := startTelegram(t, f, dir, 42)
			d.setModel(t, "spawn")
			conn := taskResultDB(t, dir)
			main := d.mainChat(t)
			next := taskJSON(t, map[string]string{"title": "Follow-up", "goal": "finished", "provider": "faux", "modelId": "echo"})
			input := taskJSON(t, map[string]string{"title": "First", "goal": "next-task: " + next, "provider": "faux", "modelId": "echo"})
			parent := main
			if origin == "web" {
				d.send(t, main, input)
			} else {
				f.message(1, 42, 7, "/new")
				waitTaskChannelText(t, f, "Context cleared.")
				f.message(2, 42, 7, input)
				eventually(t, "channel source", func() (bool, any) {
					err := conn.QueryRowContext(t.Context(), "SELECT parent_conversation_id FROM tasks WHERE title='First'").Scan(&parent)
					return err == nil, err
				})
				if parent != main {
					t.Fatal("channel input missed main")
				}
			}
			// When the first report starts the next execution, its source is a task result.
			first := waitExecution(t, conn, parent, "First", "done")
			second := waitExecution(t, conn, parent, "Follow-up", "done")
			resultID := "task:" + second.ID + ":result"
			waitResultSubmission(t, conn, resultID, "done")
			var source string
			if err := conn.QueryRowContext(t.Context(), "SELECT source_request_id FROM tasks WHERE id=?", second.ID).Scan(&source); err != nil {
				t.Fatal(err)
			}
			if source != "task:"+first.ID+":result" {
				t.Fatalf("follow-up source=%q", source)
			}
			// Then both result inputs keep the original peer and internal inputs have their own origin.
			for _, id := range []string{first.ID, second.ID} {
				assertSubmissionOrigin(t, conn, id, "task")
				assertSubmissionOrigin(t, conn, "task:"+id+":result", origin)
			}
			var sourceID string
			if err := conn.QueryRowContext(t.Context(), "SELECT source_request_id FROM tasks WHERE id=?", first.ID).Scan(&sourceID); err != nil {
				t.Fatal(err)
			}
			assertSubmissionOrigin(t, conn, sourceID, origin)
			t.Logf("artifact: origin=%s parent=%d chain=%s -> %s", origin, parent, first.ID, second.ID)
		})
	}
}

func assertSubmissionOrigin(t *testing.T, conn *sql.DB, id, want string) {
	t.Helper()
	var got string
	if err := conn.QueryRowContext(t.Context(), "SELECT origin FROM submissions WHERE id=?", id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("submission %s origin=%q want=%q", id, got, want)
	}
}

func TestUserClearOrdersWithConcurrentInput(t *testing.T) {
	// Given an idle timeline, then Clear and message admission start at the same time.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	id := d.mainChat(t)
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
			status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/clear", id), map[string]string{"id": resetID})
			cleared <- response{status, raw}
		}()
		go func() {
			<-start
			status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/messages", id), map[string]string{"id": inputID, "text": "concurrent input"})
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
	// Given a new timeline without a model, Clear does not need generation settings.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	id := d.mainChat(t)
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
	status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/clear", id), map[string]string{"id": inputID})
	if status != http.StatusBadRequest || !strings.Contains(raw, "another input") {
		t.Fatalf("request ID collision: %d %s", status, raw)
	}
}
