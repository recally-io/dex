package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

type sessionInfo struct {
	chatInfo
	Main      bool      `json:"main"`
	ReadOnly  bool      `json:"readOnly"`
	UpdatedAt int64     `json:"updatedAt"`
	Task      *taskInfo `json:"task"`
}

func (d *dex) newSession(t *testing.T) sessionInfo {
	t.Helper()
	var c sessionInfo
	d.must(t, http.MethodPost, "/api/agents/default/chats", nil, &c, http.StatusCreated)
	return c
}

func sessionDB(t *testing.T, d *dex) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(d.dataDir(t), "control.sqlite")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Stored sessions let each acceptance test reach its own boundary before POST exists.
func sessionFixture(t *testing.T, d *dex, key string, at int64) int64 {
	t.Helper()
	q := sqlc.New(sessionDB(t, d))
	c, err := q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{
		AgentID: "default", ChannelKey: key, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestUserCreatesAndReusesEmptyChatSession(t *testing.T) {
	// Given a new installation with no model, opening a session needs no message.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	main := d.mainChat(t)
	first := d.newSession(t)
	if first.ID == main || first.Main || first.ReadOnly || first.Channel != "web" || first.Title != "" {
		t.Fatalf("new Chat session: %+v", first)
	}
	if again := d.newSession(t); again.ID != first.ID {
		t.Fatalf("abandoned empty session was not reused: %+v != %+v", again, first)
	}
	if list := d.chats(t); len(list) != 0 {
		t.Fatalf("main or empty Chat entered Sessions: %+v", list)
	}
	// When several tabs open a session together, they reuse the same empty row.
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/agents/default/chats", nil)
			if err != nil {
				t.Error(err)
				return
			}
			resp, err := d.srv.Client().Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			var c sessionInfo
			if err := json.UnmarshalRead(resp.Body, &c); err != nil {
				t.Error(err)
			}
			if resp.StatusCode != http.StatusCreated || c.ID != first.ID {
				t.Errorf("concurrent open: status=%d session=%+v", resp.StatusCode, c)
			}
		})
	}
	wg.Wait()
	// Then the first message sets a 60-character title that later input preserves.
	d.setModel(t, "echo")
	text := strings.Repeat("会", 65)
	d.send(t, first.ID, text)
	d.watch(t, first.ID).until(t, "first Chat answer", answered("echo: "+text))
	d.send(t, first.ID, "keep the title")
	d.watch(t, first.ID).until(t, "second Chat answer", answered("echo: keep the title"))
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", first.ID), nil, &first)
	if first.Title != strings.Repeat("会", 59)+"…" {
		t.Fatalf("first-message title changed: %q", first.Title)
	}
	second := d.newSession(t)
	if second.ID == first.ID {
		t.Fatal("a nonempty session was reused")
	}
	d.stop()
	d = startDex(t, dir, "")
	if again := d.newSession(t); again.ID != second.ID {
		t.Fatalf("empty session was not reused after restart: %+v", again)
	}
	// Existing empty rows use creation order.
	newest := sessionFixture(t, d, "chat:newest-empty", second.UpdatedAt+1)
	if again := d.newSession(t); again.ID != newest {
		t.Fatalf("did not reuse newest empty session: %+v want=%d", again, newest)
	}
	t.Logf("artifact: main=%d titled=%d reused_after_restart=%d newest_empty=%d", main, first.ID, second.ID, newest)
}

func TestUserChatContextAndClearStaySeparateFromMain(t *testing.T) {
	// Given main and a Chat session using the real worker's context provider.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "context")
	main := d.mainChat(t)
	side := sessionFixture(t, d, "chat:context", 1)
	d.send(t, main, "main-only context")
	mw := d.watch(t, main)
	mw.until(t, "main context", answered(`context: ["main-only context"]`))
	// When the user writes in Chat, the model gets only Chat input.
	d.send(t, side, "side-only context")
	sw := d.watch(t, side)
	sw.until(t, "isolated Chat model context", answered(`context: ["side-only context"]`))
	before := slices.Clone(sw.tr.items)
	d.clearMain(t, side, uuid.NewV7().String())
	sw.until(t, "Chat reset", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "boundary" })
	})
	d.send(t, side, "fresh side context")
	sw.until(t, "reset Chat model context", answered(`context: ["fresh side context"]`))
	// Then main retains its own context, and Chat history retains its earlier messages.
	d.send(t, main, "still main")
	mw.until(t, "unchanged main context", answered(`context: ["main-only context","still main"]`))
	d.clearMain(t, main, uuid.NewV7().String())
	d.send(t, main, "fresh main context")
	mw.until(t, "reset main model context", answered(`context: ["fresh main context"]`))
	d.send(t, side, "still side")
	sw.until(t, "main Clear preserves Chat context", answered(`context: ["fresh side context","still side"]`))
	var page struct {
		Items []item `json:"items"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/history?before=9007199254740991&limit=100", side), nil, &page)
	for _, it := range before {
		if !slices.ContainsFunc(page.Items, func(got item) bool { return got.ID == it.ID && got.Text == it.Text }) {
			t.Fatalf("Chat history lost %+v: %+v", it, page.Items)
		}
	}
	t.Logf("artifact: main_context=%+v chat_context=%+v chat_history=%+v", mw.tr.items, sw.tr.items, page.Items)
}

func TestUserTaskResultReturnsToItsChatSession(t *testing.T) {
	// Given a Chat and main on the same bot, with a task started in Chat.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	main := d.mainChat(t)
	side := sessionFixture(t, d, "chat:task-parent", 1)
	conn := taskResultDB(t, dir)
	d.send(t, side, taskJSON(t, map[string]string{
		"title": "Chat report", "goal": "only for this Chat", "provider": "faux", "modelId": "echo",
	}))
	// When the worker returns the report, only the source Chat gets the result turn.
	task := waitExecution(t, conn, side, "Chat report", "done")
	resultID := "task:" + task.ID + ":result"
	w := d.watch(t, side)
	w.until(t, "Chat result answer", answeredWith(`task-result: {"id":"`+task.ID+`"`))
	waitResultSubmission(t, conn, resultID, "done")
	var parent int64
	var origin string
	if err := conn.QueryRowContext(t.Context(), "SELECT conversation_id, origin FROM submissions WHERE id=?", resultID).Scan(&parent, &origin); err != nil {
		t.Fatal(err)
	}
	if parent != side || origin != "web" || !slices.ContainsFunc(w.tr.items, func(it item) bool { return it.Kind == "task" && it.TaskID == task.ID }) {
		t.Fatalf("Chat result routing: parent=%d origin=%s transcript=%+v", parent, origin, w.tr.items)
	}
	mw := d.watch(t, main)
	mw.until(t, "main snapshot", func(tr *transcript) bool { return tr.snapshots > 0 })
	if len(mw.tr.items) != 0 {
		t.Fatalf("Chat result leaked into main: %+v", mw.tr.items)
	}
	var detail taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &detail)
	if detail.ParentConversationID != side || detail.Report != "echo: only for this Chat" {
		t.Fatalf("Chat task detail: %+v", detail)
	}
	t.Logf("artifact: main=%d chat=%d task=%s result=%s origin=%s", main, side, task.ID, resultID, origin)
}

func TestUserStopAndClearRespectChatSessionScope(t *testing.T) {
	// Given main and Chat, each with background work in a real gated shell.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	main := d.mainChat(t)
	side := sessionFixture(t, d, "chat:stop", 1)
	conn := taskResultDB(t, dir)
	for _, c := range []struct {
		id   int64
		gate string
	}{{main, "scope-main-task"}, {side, "scope-chat-task"}} {
		d.send(t, c.id, taskJSON(t, map[string]string{"title": c.gate, "goal": taskGate(t, dir, c.gate), "provider": "faux", "modelId": "bash"}))
		waitTaskFile(t, filepath.Join(dir, c.gate+".pid"))
		waitExecution(t, conn, c.id, c.gate, "running")
	}
	// When Chat Stop withdraws a running Chat turn and its queued follow-up.
	d.setModel(t, "bash")
	d.send(t, main, taskGate(t, dir, "scope-main-turn"))
	waitTaskFile(t, filepath.Join(dir, "scope-main-turn.pid"))
	d.send(t, side, taskGate(t, dir, "scope-chat-turn"))
	waitTaskFile(t, filepath.Join(dir, "scope-chat-turn.pid"))
	queued := d.send(t, side, "echo must-not-run")
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/clear", side), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusConflict)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", side), nil, nil)
	waitExecution(t, conn, side, "scope-chat-task", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "scope-chat-task.pid"))
	waitTaskPIDGone(t, filepath.Join(dir, "scope-chat-turn.pid"))
	waitResultSubmission(t, conn, queued, "unanswered")
	// Then main keeps running, and an idle Chat can clear while main is busy.
	waitExecution(t, conn, main, "scope-main-task", "running")
	for _, gate := range []string{"scope-main-task", "scope-main-turn"} {
		if taskPIDGone(t, filepath.Join(dir, gate+".pid")) {
			t.Fatalf("Chat Stop crossed into %s", gate)
		}
	}
	d.clearMain(t, side, uuid.NewV7().String())
	d.watch(t, side).until(t, "Chat clear while main is busy", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "boundary" })
	})
	// A later main Stop must also leave Chat's next task and foreground turn alone.
	d.setModel(t, "spawn")
	d.send(t, side, taskJSON(t, map[string]string{"title": "scope-chat-next", "goal": taskGate(t, dir, "scope-chat-next"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "scope-chat-next.pid"))
	waitExecution(t, conn, side, "scope-chat-next", "running")
	d.setModel(t, "bash")
	d.send(t, side, taskGate(t, dir, "scope-chat-next-turn"))
	waitTaskFile(t, filepath.Join(dir, "scope-chat-next-turn.pid"))
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", main), nil, nil)
	waitExecution(t, conn, main, "scope-main-task", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "scope-main-task.pid"))
	waitTaskPIDGone(t, filepath.Join(dir, "scope-main-turn.pid"))
	waitExecution(t, conn, side, "scope-chat-next", "running")
	for _, gate := range []string{"scope-chat-next", "scope-chat-next-turn"} {
		if taskPIDGone(t, filepath.Join(dir, gate+".pid")) {
			t.Fatalf("main Stop crossed into %s", gate)
		}
	}
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", side), nil, nil)
	waitTaskPIDGone(t, filepath.Join(dir, "scope-chat-next.pid"))
	waitTaskPIDGone(t, filepath.Join(dir, "scope-chat-next-turn.pid"))
	t.Logf("artifact: stopped_chat=%d preserved_main=%d withdrawn=%s", side, main, queued)
}

func TestUserCannotWriteReadOnlySessionKinds(t *testing.T) {
	// Given task, automation and channel sessions owned by the bot.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	for _, key := range []string{"task:readonly", "automation:readonly", "telegram:7", "wechat:8", "feishu:9"} {
		id := sessionFixture(t, d, key, 1)
		var c sessionInfo
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", id), nil, &c)
		if !c.ReadOnly {
			t.Fatalf("writable read-only kind: %+v", c)
		}
		// When the web sends input or requests Stop or Clear, it gets 403.
		for _, action := range []string{"messages", "clear", "abort"} {
			d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/%s", id, action), map[string]string{"id": uuid.NewV7().String(), "text": "must not run"}, nil, http.StatusForbidden)
		}
	}
}

func TestUserSessionListShowsOrderedSummariesOnly(t *testing.T) {
	// Given saved sessions with equal activity times, task reports and hidden contexts.
	d := startDex(t, t.TempDir(), "")
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	dream := sessionFixture(t, d, "dream:hidden", 900)
	_ = sessionFixture(t, d, "chat:empty", 1000)
	chat := sessionFixture(t, d, "chat:listed", 1)
	automation := sessionFixture(t, d, "automation:listed", 3)
	channel := sessionFixture(t, d, "telegram:listed", 4)
	task := sessionFixture(t, d, "task:listed", 5)
	for _, c := range []struct{ id, at int64 }{{main, 1000}, {dream, 1000}, {chat, 200}, {automation, 100}, {channel, 200}, {task, 150}} {
		id := fmt.Sprint(c.id)
		if c.id == task {
			id = "listed-task"
		}
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO submissions (id, conversation_id, content, attachments, kind, status, created_at) VALUES (?, ?, 'private transcript', 'null', 'message', 'done', ?)", id, c.id, c.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO tasks (id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, status, report, created_at) VALUES ('listed-task', ?, ?, 'listed-spawn', ?, 'Listed task', 'done', 'private report body', 5)", chat, task, fmt.Sprint(chat)); err != nil {
		t.Fatal(err)
	}
	// When the caller lists Sessions, rows follow activity time with a stable ID tie-break.
	status, raw := d.call(t, http.MethodGet, "/api/agents/default/chats", nil)
	if status != http.StatusOK || strings.Contains(raw, `"report":`) || strings.Contains(raw, "private report") || strings.Contains(raw, "private transcript") || strings.Contains(raw, `"items":`) {
		t.Fatalf("session summaries: %d %s", status, raw)
	}
	var list struct {
		Chats []sessionInfo `json:"chats"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, c := range list.Chats {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []int64{chat, task, automation}) {
		t.Fatalf("session list order or exclusions: %v; body=%s", ids, raw)
	}
	if list.Chats[1].Task == nil || list.Chats[1].Task.ID != "listed-task" || list.Chats[1].Task.ParentConversationID != chat {
		t.Fatalf("task summary or Stop scope missing: %s", raw)
	}
	for _, c := range list.Chats {
		if c.ID != task && c.Task != nil {
			t.Fatalf("non-task has a task summary: %+v", c)
		}
	}
	// Then task detail keeps the report, and the old task-list API is gone.
	var detail taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/listed-task", nil, &detail)
	if detail.Report != "private report body" {
		t.Fatalf("task detail lost report: %+v", detail)
	}
	status, raw = d.call(t, http.MethodGet, "/api/agents/default/tasks", nil)
	if status != http.StatusNotFound {
		t.Fatalf("obsolete task list still exists: %d %s", status, raw)
	}
	t.Logf("artifact: listed_ids=%v task_detail=%+v", ids, detail)
}

func TestUserSeesSessionActivityThroughTaskCompletion(t *testing.T) {
	// Given an old task and newer Chats with equal completion times.
	d := startDex(t, t.TempDir(), "")
	conn := sessionDB(t, d)
	main := sessionFixture(t, d, "main", 1)
	task := sessionFixture(t, d, "task:old", 100)
	chat := sessionFixture(t, d, "chat:newer", 200)
	tiedChat := sessionFixture(t, d, "chat:tied", 201)
	for _, c := range []struct{ id, created, settled int64 }{
		{main, 50, 60}, {task, 100, 300}, {chat, 200, 210}, {tiedChat, 201, 210},
	} {
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO submissions (id, conversation_id, content, status, created_at, settled_at) VALUES (?, ?, 'saved input', 'done', ?, ?)", fmt.Sprint(c.id), c.id, c.created, c.settled); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO tasks (id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, status, created_at, started_at, ended_at) VALUES (?, ?, ?, 'old-spawn', ?, 'Old task', 'done', 100, 110, 300)", fmt.Sprint(task), main, task, fmt.Sprint(main)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		settled *int64
		started int64
		ended   *int64
		status  string
		want    int64
	}{
		{"input_settlement", new(int64(300)), 110, new(int64(290)), "done", 300},
		{"task_end", nil, 110, new(int64(310)), "done", 310},
		{"task_start", nil, 320, nil, "running", 320},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When task activity occurs after both newer Chats finish.
			if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET settled_at = ? WHERE conversation_id = ?", tc.settled, task); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), "UPDATE tasks SET started_at = ?, ended_at = ?, status = ? WHERE conversation_id = ?", tc.started, tc.ended, tc.status, task); err != nil {
				t.Fatal(err)
			}
			var list struct {
				Chats []sessionInfo `json:"chats"`
			}
			d.must(t, http.MethodGet, "/api/agents/default/chats", nil, &list)
			var ids []int64
			for _, c := range list.Chats {
				ids = append(ids, c.ID)
			}
			// Then the old task leads, and equal times use descending IDs.
			if !slices.Equal(ids, []int64{task, tiedChat, chat}) {
				t.Errorf("completed old task must sort first: got=%v want=%v", ids, []int64{task, tiedChat, chat})
			}
			for _, c := range list.Chats {
				want := int64(210)
				if c.ID == task {
					want = tc.want
				}
				var detail sessionInfo
				d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", c.ID), nil, &detail)
				if c.UpdatedAt != want || detail.UpdatedAt != want {
					t.Errorf("session %d activity: list=%d detail=%d want=%d", c.ID, c.UpdatedAt, detail.UpdatedAt, want)
				}
			}
			// Main uses its own settlement, without inheriting child task times.
			for _, path := range []string{"/api/agents/default/chats/main", fmt.Sprintf("/api/agents/default/chats/%d", main)} {
				var c sessionInfo
				d.must(t, http.MethodGet, path, nil, &c)
				if c.ID != main || !c.Main || c.UpdatedAt != 60 {
					t.Errorf("main activity through %s: %+v; want updatedAt=60", path, c)
				}
			}
			t.Logf("artifact: ids=%v task_activity=%d chat_activity=210 main_activity=60", ids, tc.want)
		})
	}
}
