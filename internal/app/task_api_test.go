package app_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
)

func TestUserUpdatesTaskByKeyAndUUID(t *testing.T) {
	t.Parallel()
	// Given an Owner Task that requires review.
	d := startDex(t, t.TempDir(), "")
	var task api.TaskSummary
	const tasks = "/api/tasks"
	d.must(t, http.MethodPost, tasks, map[string]string{
		"title": "Ship the Board", "assigneeBotId": "default", "status": "in_progress", "reviewerKind": "owner",
	}, &task, http.StatusCreated)
	keyPath, uuidPath := tasks+"/"+task.Key, tasks+"/"+task.Id
	for _, path := range []string{keyPath, uuidPath} {
		var detail api.TaskDetail
		d.must(t, http.MethodGet, path, nil, &detail)
		if task.Key != fmt.Sprintf("T-%d", task.Number) || task.Number < 1 || detail.Task.Id != task.Id {
			t.Fatalf("unexpected Task identity: %+v, detail %+v", task, detail)
		}
	}

	// When the Owner submits the Task and another tab still has the old revision.
	old := task.Revision
	withRevision := func(method, path string, revision int64, want int, body string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, d.srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", strconv.FormatInt(revision, 10))
		res, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s %s: %d %s (%v)", method, path, res.StatusCode, raw, err)
		}
	}
	withRevision(http.MethodPatch, keyPath, old, http.StatusOK, `{"status":"in_review"}`)
	withRevision(http.MethodPatch, keyPath, old, http.StatusPreconditionFailed, `{"title":"Stale title"}`)
	withRevision(http.MethodPost, uuidPath+"/entries", old, http.StatusPreconditionFailed, `{"content":"Stale note"}`)
	d.must(t, http.MethodPost, uuidPath+"/entries", map[string]string{"kind": "assign", "content": "Forged entry"}, nil, http.StatusBadRequest)
	var revised api.TaskDetail
	d.must(t, http.MethodGet, keyPath, nil, &revised)
	if revised.Task.Title != task.Title || revised.Task.ReviewSeq <= 0 {
		t.Fatalf("unexpected review: %+v", revised)
	}

	// Then the Owner can request changes, and approval for the first submission cannot complete the second.
	reviewPath := uuidPath + "/review"
	d.must(t, http.MethodPost, reviewPath, map[string]string{"decision": "request_changes", "note": "Add tests"}, &task)
	if task.Status != "in_progress" {
		t.Fatalf("status = %s after request changes", task.Status)
	}
	withRevision(http.MethodPatch, keyPath, task.Revision, http.StatusOK, `{"status":"in_review"}`)
	d.must(t, http.MethodPost, reviewPath, map[string]any{"decision": "approve", "reviewSeq": revised.Task.ReviewSeq}, nil, http.StatusConflict)
	d.must(t, http.MethodGet, keyPath, nil, &revised)
	d.must(t, http.MethodPost, reviewPath, map[string]any{"decision": "approve", "reviewSeq": revised.Task.ReviewSeq}, &task)
	if task.Status != "done" {
		t.Fatalf("status = %s, want done", task.Status)
	}
	var list api.TaskList
	d.must(t, http.MethodGet, tasks, nil, &list)
	if len(list.Tasks) != 0 {
		t.Fatalf("default Board shows closed Tasks: %+v", list)
	}
	d.must(t, http.MethodGet, tasks+"?includeClosed=true&status=done&botId=default", nil, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].Id != task.Id {
		t.Fatalf("filtered Board: %+v", list)
	}
}

func TestUserResumesTaskEventsWithoutGaps(t *testing.T) {
	t.Parallel()
	// Given a Task with one committed entry.
	d := startDex(t, t.TempDir(), "")
	var task api.TaskSummary
	d.must(t, http.MethodPost, "/api/tasks", map[string]string{"title": "Live Board", "assigneeBotId": "default"}, &task, http.StatusCreated)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	open := func(after string) (*bufio.Scanner, io.Closer) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.srv.URL+"/api/tasks/events?after=0", nil)
		if err != nil {
			t.Fatal(err)
		}
		if after != "" {
			req.Header.Set("Last-Event-ID", after)
		}
		res, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
			_ = res.Body.Close()
			t.Fatalf("stream status %d, Content-Type %q", res.StatusCode, res.Header.Get("Content-Type"))
		}
		return bufio.NewScanner(res.Body), res.Body
	}
	entry := func(scan *bufio.Scanner) int64 {
		t.Helper()
		var id int64
		for scan.Scan() {
			if raw, ok := strings.CutPrefix(scan.Text(), "id: "); ok {
				var err error
				id, err = strconv.ParseInt(raw, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scan.Text() == "event: task_entry" && id > 0 {
				return id
			}
		}
		t.Fatalf("missing Board entry: %v", scan.Err())
		return 0
	}
	scan, body := open("")
	defer body.Close()
	first := entry(scan)
	_ = body.Close()

	// When more than one catch-up page commits offline and the next note commits after reconnect.
	post := func(content string) {
		d.must(t, http.MethodPost, "/api/tasks/"+task.Key+"/entries", map[string]string{"content": content}, nil, http.StatusCreated)
	}
	const offline = 130
	for range offline {
		post("Offline")
	}
	scan, body = open(strconv.FormatInt(first, 10))
	defer body.Close()
	// Then the header overrides the URL cursor and all entries arrive in order.
	for want := first + 1; want <= first+offline; want++ {
		if got := entry(scan); got != want {
			t.Fatalf("catch-up ID = %d, want %d", got, want)
		}
	}
	post("Online")
	if got, want := entry(scan), first+offline+1; got != want {
		t.Fatalf("live ID = %d, want %d", got, want)
	}
}

func TestUserReassignsBlockedTaskAndAnswersInOriginalMain(t *testing.T) {
	t.Parallel()
	// Given an open question on the current Assignee's Main.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	coder := d.createCoder(t)
	var main sessionListItem
	d.must(t, http.MethodGet, "/api/bots/default/sessions/main", nil, &main)
	const tasks = "/api/tasks"
	var task api.TaskSummary
	d.must(t, http.MethodPost, tasks, map[string]string{"title": "Choose a fix", "assigneeBotId": "default"}, &task, http.StatusCreated)
	path := fmt.Sprintf("/api/bots/default/sessions/%d/messages", main.ID)
	d.must(t, http.MethodPost, path, map[string]string{"id": uuid.NewV7().String(), "text": askJSON, "taskId": task.Id}, nil, http.StatusAccepted)
	question := d.awaitQuestion(t, main.ID, "open")
	d.awaitIdle(t, main.ID)

	// When the Owner changes the Assignee, the new Main gets one assigned wake.
	taskPath := tasks + "/" + task.Key
	d.must(t, http.MethodPatch, taskPath, map[string]string{"assigneeBotId": coder.ID}, &task)
	var newMain sessionListItem
	d.must(t, http.MethodGet, "/api/bots/"+coder.ID+"/sessions/main", nil, &newMain)
	w := d.watchPath(t, fmt.Sprintf("/api/bots/%s/sessions/%d/events", coder.ID, newMain.ID))
	w.until(t, "assigned wake on the new Main", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "task_wake" && it.Title == "Task wake" })
	})
	var list api.TaskList
	d.must(t, http.MethodGet, tasks+"?blocked=true&botId="+coder.ID, nil, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].Id != task.Id || list.Tasks[0].BlockerCount != 1 {
		t.Fatalf("the open question must still block the reassigned Task: %+v", list.Tasks)
	}
	var detail api.TaskDetail
	d.must(t, http.MethodGet, taskPath, nil, &detail)
	if len(detail.Blockers) != 1 || detail.Blockers[0].QueuePosition == nil || *detail.Blockers[0].QueuePosition != 1 ||
		len(detail.Questions) != 1 || detail.Questions[0].SessionId != main.ID ||
		!slices.ContainsFunc(detail.Entries, func(e api.TaskEntry) bool { return e.Kind == "assign" }) {
		t.Fatalf("missing original question or assignment entry: %+v", detail)
	}

	// Then the question is still answerable in its original Main.
	d.setModel(t, "echo")
	d.answer(t, main.ID, question.ID, "Investigate only", http.StatusAccepted)
	d.must(t, http.MethodGet, taskPath, nil, &detail)
	if detail.Task.Blocked || len(detail.Questions) != 0 ||
		!slices.ContainsFunc(detail.Entries, func(e api.TaskEntry) bool { return e.Kind == "note" && e.Content == "Investigate only" }) {
		t.Fatalf("the answer did not clear the blocker and update the Task: %+v", detail)
	}
}

func TestUserTaskEventsRequireTheAccessToken(t *testing.T) {
	t.Parallel()
	// Given a server with an access token.
	const token = "board-test-token"
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), Token: token})
	const path = "/api/tasks/events"

	// An unsigned request cannot open the stream.
	d.must(t, http.MethodGet, path, nil, nil, http.StatusUnauthorized)

	// The authorized client can open the stream.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("authorized stream: %d %v", res.StatusCode, res.Header)
	}
}
