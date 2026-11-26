package app_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

type sessionInfo struct {
	sessionListItem
	Main      bool   `json:"main"`
	ReadOnly  bool   `json:"readOnly"`
	UpdatedAt int64  `json:"updatedAt"`
	ParentID  *int64 `json:"parentId"`
}

func (d *dex) newSession(t *testing.T) sessionInfo {
	t.Helper()
	var c sessionInfo
	d.must(t, http.MethodPost, "/api/bots/default/sessions", nil, &c, http.StatusCreated)
	return c
}

// Stored sessions let each acceptance test reach its own boundary before POST exists.
func sessionFixture(t *testing.T, d *dex, key string, at int64) int64 {
	t.Helper()
	q := sqlc.New(sessionDB(t, d))
	c, err := q.EnsureSession(t.Context(), sqlc.EnsureSessionParams{
		BotID: "default", ChannelKey: key, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestUserCreatesAndReusesEmptySessionSession(t *testing.T) {
	t.Parallel()
	// Given a new installation with no model, opening a session needs no message.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	main := d.mainSession(t)
	first := d.newSession(t)
	if first.ID == main || first.Main || first.ReadOnly || first.Channel != "web" || first.Title != "" {
		t.Fatalf("new session: %+v", first)
	}
	if again := d.newSession(t); again.ID != first.ID {
		t.Fatalf("abandoned empty session was not reused: %+v != %+v", again, first)
	}
	if list := d.sessions(t); len(list) != 0 {
		t.Fatalf("main or empty Session entered Sessions: %+v", list)
	}
	// When several tabs open a session together, they reuse the same empty row.
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/bots/default/sessions", nil)
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
	d.watch(t, first.ID).until(t, "first Session answer", answered("echo: "+text))
	d.send(t, first.ID, "keep the title")
	d.watch(t, first.ID).until(t, "second Session answer", answered("echo: keep the title"))
	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d", first.ID), nil, &first)
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

func TestUserSessionContextAndClearStaySeparateFromMain(t *testing.T) {
	t.Parallel()
	// Given main and a session using the real worker's context provider.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "context")
	main := d.mainSession(t)
	side := sessionFixture(t, d, "chat:context", 1)
	d.send(t, main, "main-only context")
	mw := d.watch(t, main)
	mw.until(t, "main context", answered(`context: ["main-only context"]`))
	// When the user writes in Session, the model gets only Session input.
	d.send(t, side, "side-only context")
	sw := d.watch(t, side)
	sw.until(t, "isolated Session model context", answered(`context: ["side-only context"]`))
	before := slices.Clone(sw.tr.items)
	d.clearMain(t, side, uuid.NewV7().String())
	sw.until(t, "Session reset", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "boundary" })
	})
	d.send(t, side, "fresh side context")
	sw.until(t, "reset Session model context", answered(`context: ["fresh side context"]`))
	// Then main retains its own context, and Session history retains its earlier messages.
	d.send(t, main, "still main")
	mw.until(t, "unchanged main context", answered(`context: ["main-only context","still main"]`))
	d.clearMain(t, main, uuid.NewV7().String())
	d.send(t, main, "fresh main context")
	mw.until(t, "reset main model context", answered(`context: ["fresh main context"]`))
	d.send(t, side, "still side")
	sw.until(t, "main Clear preserves Session context", answered(`context: ["fresh side context","still side"]`))
	var page struct {
		Items []item `json:"items"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d/history?before=9007199254740991&limit=100", side), nil, &page)
	for _, it := range before {
		if !slices.ContainsFunc(page.Items, func(got item) bool { return got.ID == it.ID && got.Text == it.Text }) {
			t.Fatalf("Session history lost %+v: %+v", it, page.Items)
		}
	}
	t.Logf("artifact: main_context=%+v chat_context=%+v chat_history=%+v", mw.tr.items, sw.tr.items, page.Items)
}

func TestUserChildResultReturnsToItsSessionSession(t *testing.T) {
	t.Parallel()
	// Given a Session and main on the same bot, with a child session started in Session.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	main := d.mainSession(t)
	side := sessionFixture(t, d, "chat:task-parent", 1)
	conn := taskResultDB(t, dir)
	d.send(t, side, childJSON(t, map[string]string{
		"title": "Session report", "message": "only for this Session", "provider": "faux", "modelId": "echo",
	}))
	// When the child returns its answer, only the source Session gets the result turn.
	child := waitExecution(t, conn, side, "Session report", "done")
	rows := waitResults(t, conn, side, 1)
	if rows[0].id != child.ResultID {
		t.Fatalf("result %q, want %q", rows[0].id, child.ResultID)
	}
	w := d.watch(t, side)
	w.until(t, "Session result answer", answeredWith("session-result: "))
	var parent int64
	var origin string
	if err := conn.QueryRowContext(t.Context(), "SELECT session_id, origin FROM submissions WHERE id=?", child.ResultID).Scan(&parent, &origin); err != nil {
		t.Fatal(err)
	}
	if parent != side || origin != "task" || !slices.ContainsFunc(w.tr.items, func(it item) bool {
		return it.Kind == "session_result" && it.ID == child.ResultID && it.Title == "Session report" && it.Text == "" && it.Status == "done"
	}) {
		t.Fatalf("Session result routing: parent=%d origin=%s transcript=%+v", parent, origin, w.tr.items)
	}
	var page struct {
		Items []item `json:"items"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d/history?before=9007199254740991&limit=100", side), nil, &page)
	if !slices.ContainsFunc(page.Items, func(it item) bool {
		return it.Kind == "session_result" && it.ID == child.ResultID && it.Text == "" && it.Status == "done"
	}) {
		t.Fatalf("history lost child result status: %+v", page.Items)
	}
	mw := d.watch(t, main)
	mw.until(t, "main snapshot", func(tr *transcript) bool { return tr.snapshots > 0 })
	if len(mw.tr.items) != 0 {
		t.Fatalf("Session result leaked into main: %+v", mw.tr.items)
	}
	t.Logf("artifact: main=%d session=%d child=%s result=%s origin=%s", main, side, child.ID, child.ResultID, origin)
}

func TestUserStopAndClearRespectSessionSessionScope(t *testing.T) {
	t.Parallel()
	// Given main and Session, each with background work in a real gated shell.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	main := d.mainSession(t)
	side := sessionFixture(t, d, "chat:stop", 1)
	conn := taskResultDB(t, dir)
	for _, c := range []struct {
		id   int64
		gate string
	}{{main, "scope-main-task"}, {side, "scope-session-task"}} {
		d.send(t, c.id, childJSON(t, map[string]any{"title": c.gate, "message": taskGate(t, dir, c.gate), "provider": "faux", "modelId": "bash"}))
		waitTaskFile(t, filepath.Join(dir, c.gate+".pid"))
		waitExecution(t, conn, c.id, c.gate, "running")
	}
	// When Session Stop withdraws a running Session turn and its queued follow-up.
	d.setModel(t, "bash")
	d.send(t, main, taskGate(t, dir, "scope-main-turn"))
	waitTaskFile(t, filepath.Join(dir, "scope-main-turn.pid"))
	d.send(t, side, taskGate(t, dir, "scope-session-turn"))
	waitTaskFile(t, filepath.Join(dir, "scope-session-turn.pid"))
	queued := d.send(t, side, "echo must-not-run")
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/clear", side), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusConflict)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/abort", side), nil, nil)
	waitExecution(t, conn, side, "scope-session-task", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "scope-session-task.pid"))
	waitTaskPIDGone(t, filepath.Join(dir, "scope-session-turn.pid"))
	waitResultSubmission(t, conn, queued, "unanswered")
	// Then main keeps running, and an idle Session can clear while main is busy.
	waitExecution(t, conn, main, "scope-main-task", "running")
	for _, gate := range []string{"scope-main-task", "scope-main-turn"} {
		if taskPIDGone(t, filepath.Join(dir, gate+".pid")) {
			t.Fatalf("Session Stop crossed into %s", gate)
		}
	}
	d.clearMain(t, side, uuid.NewV7().String())
	d.watch(t, side).until(t, "Session clear while main is busy", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "boundary" })
	})
	// A later Main Stop keeps its queued follow-up, its child, and the other Session's work.
	d.setModel(t, "spawn")
	d.send(t, side, childJSON(t, map[string]any{"title": "scope-session-next", "message": taskGate(t, dir, "scope-session-next"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "scope-session-next.pid"))
	waitExecution(t, conn, side, "scope-session-next", "running")
	d.setModel(t, "bash")
	d.send(t, side, taskGate(t, dir, "scope-session-next-turn"))
	waitTaskFile(t, filepath.Join(dir, "scope-session-next-turn.pid"))
	mainQueued := d.send(t, main, "echo main-kept")
	target := d.watch(t, main).activeRunTarget(t)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/abort?runTarget=%s", main, url.QueryEscape(target)), nil, nil)
	waitTaskPIDGone(t, filepath.Join(dir, "scope-main-turn.pid"))
	waitResultSubmission(t, conn, mainQueued, "done")
	waitExecution(t, conn, main, "scope-main-task", "running")
	releaseResultGate(t, dir, "scope-main-task")
	waitExecution(t, conn, main, "scope-main-task", "done")
	waitExecution(t, conn, side, "scope-session-next", "running")
	for _, gate := range []string{"scope-session-next", "scope-session-next-turn"} {
		if taskPIDGone(t, filepath.Join(dir, gate+".pid")) {
			t.Fatalf("main Stop crossed into %s", gate)
		}
	}
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/abort", side), nil, nil)
	waitTaskPIDGone(t, filepath.Join(dir, "scope-session-next.pid"))
	waitTaskPIDGone(t, filepath.Join(dir, "scope-session-next-turn.pid"))
	t.Logf("artifact: stopped_chat=%d preserved_main=%d withdrawn=%s", side, main, queued)
}

func TestUserSessionListShowsOrderedSummariesOnly(t *testing.T) {
	t.Parallel()
	// Given saved sessions with equal activity times, a child session from before child sessions and hidden contexts.
	d := startDex(t, t.TempDir(), "")
	conn := sessionDB(t, d)
	main := d.mainSession(t)
	dream := sessionFixture(t, d, "dream:hidden", 900)
	_ = sessionFixture(t, d, "chat:empty", 1000)
	session := sessionFixture(t, d, "chat:listed", 1)
	automation := sessionFixture(t, d, "automation:listed", 3)
	channel := sessionFixture(t, d, "telegram:listed", 4)
	task := sessionFixture(t, d, "task:listed", 5)
	for _, c := range []struct{ id, at int64 }{{main, 1000}, {dream, 1000}, {session, 200}, {automation, 100}, {channel, 200}, {task, 150}} {
		id := fmt.Sprint(c.id)
		if c.id == task {
			id = "listed-task"
		}
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO submissions (id, session_id, content, attachments, kind, status, created_at) VALUES (?, ?, 'private transcript', 'null', 'message', 'done', ?)", id, c.id, c.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE sessions SET parent_id = ? WHERE id = ?", session, task); err != nil {
		t.Fatal(err)
	}
	// When the caller lists Sessions, rows follow activity time with a stable ID tie-break.
	status, raw := d.call(t, http.MethodGet, "/api/bots/default/sessions", nil)
	if status != http.StatusOK || strings.Contains(raw, `"report":`) || strings.Contains(raw, "private report") || strings.Contains(raw, "private transcript") || strings.Contains(raw, `"items":`) {
		t.Fatalf("session summaries: %d %s", status, raw)
	}
	var list struct {
		Sessions []sessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, c := range list.Sessions {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []int64{session, task, automation}) {
		t.Fatalf("session list order or exclusions: %v; body=%s", ids, raw)
	}
	// Then the child shows its parent as a Session.
	for _, c := range list.Sessions {
		if (c.ID == task) != (c.ParentID != nil && *c.ParentID == session) {
			t.Fatalf("parentId on session %d: %v; body=%s", c.ID, c.ParentID, raw)
		}
	}
	if strings.Contains(raw, `"task"`) || strings.Contains(raw, `"channel":"task"`) {
		t.Fatalf("task fields remain in the list: %s", raw)
	}
	t.Logf("artifact: listed_ids=%v", ids)
}

func TestUserSeesSessionActivityThroughSettlement(t *testing.T) {
	t.Parallel()
	// Given an old child session and newer Sessions with equal completion times.
	d := startDex(t, t.TempDir(), "")
	conn := sessionDB(t, d)
	main := sessionFixture(t, d, "main", 1)
	task := sessionFixture(t, d, "task:old", 100)
	session := sessionFixture(t, d, "chat:newer", 200)
	tiedSession := sessionFixture(t, d, "chat:tied", 201)
	for _, c := range []struct{ id, created, settled int64 }{
		{main, 50, 60}, {task, 100, 300}, {session, 200, 210}, {tiedSession, 201, 210},
	} {
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO submissions (id, session_id, content, status, created_at, settled_at) VALUES (?, ?, 'saved input', 'done', ?, ?)", fmt.Sprint(c.id), c.id, c.created, c.settled); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE sessions SET parent_id = ? WHERE id = ?", main, task); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		settled *int64
		want    int64
	}{
		{"input_settlement", new(int64(300)), 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When child activity occurs after both newer Sessions finish.
			if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET settled_at = ? WHERE session_id = ?", tc.settled, task); err != nil {
				t.Fatal(err)
			}
			var list struct {
				Sessions []sessionInfo `json:"sessions"`
			}
			d.must(t, http.MethodGet, "/api/bots/default/sessions", nil, &list)
			var ids []int64
			for _, c := range list.Sessions {
				ids = append(ids, c.ID)
			}
			// Then the old task leads, and equal times use descending IDs.
			if !slices.Equal(ids, []int64{task, tiedSession, session}) {
				t.Errorf("completed old task must sort first: got=%v want=%v", ids, []int64{task, tiedSession, session})
			}
			for _, c := range list.Sessions {
				want := int64(210)
				if c.ID == task {
					want = tc.want
				}
				var detail sessionInfo
				d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d", c.ID), nil, &detail)
				if c.UpdatedAt != want || detail.UpdatedAt != want {
					t.Errorf("session %d activity: list=%d detail=%d want=%d", c.ID, c.UpdatedAt, detail.UpdatedAt, want)
				}
			}
			// Main uses its own settlement, without inheriting child times.
			for _, path := range []string{"/api/bots/default/sessions/main", fmt.Sprintf("/api/bots/default/sessions/%d", main)} {
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
