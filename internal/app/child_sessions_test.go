package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"uuid"
)

// The tests in this file drive real PiG, real SQLite files, and the faux provider. The parent
// chat uses the model "spawn": its message is the JSON arguments of one session tool.

type childRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type resultRow struct {
	id, status, content, answer string
}

func childJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// callSessionTool lets the parent chat call one session tool through the model "spawn".
func callSessionTool(t *testing.T, d *dex, chatID int64, tool string, args map[string]any) string {
	t.Helper()
	d.setModel(t, "spawn")
	args["tool"] = tool
	id := d.send(t, chatID, childJSON(t, args))
	w := d.watch(t, chatID)
	var answer string
	// Results of other children can follow this turn. The answer of this turn is the first
	// assistant item after its input, and it is complete when the run is idle.
	w.until(t, "tool answer to "+id, func(tr *transcript) bool {
		if tr.running {
			return false
		}
		for i, entry := range tr.items {
			if entry.ID == id && entry.Kind == "user" {
				for _, next := range tr.items[i+1:] {
					if next.Kind == "assistant" && strings.HasPrefix(next.Text, "spawn") {
						answer = next.Text
						return true
					}
				}
			}
		}
		return false
	})
	return answer
}

func toolJSON(t *testing.T, answer string, out any) {
	t.Helper()
	body, ok := strings.CutPrefix(html.UnescapeString(answer), "spawn: ")
	if !ok {
		t.Fatalf("tool failed: %s", answer)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		t.Fatalf("tool result %q: %v", body, err)
	}
}

// newParent starts a chat whose first turn is an ordinary answer.
func newParent(t *testing.T, d *dex) int64 {
	t.Helper()
	d.setModel(t, "echo")
	parent := d.newChat(t, "parent")
	d.watch(t, parent).until(t, "first answer", answeredWith("echo: parent"))
	return parent
}

// spawnChild asks the parent chat to create a child session on a faux model.
func spawnChild(t *testing.T, d *dex, parent int64, title, message, model string) childRef {
	t.Helper()
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_new", map[string]any{"title": title, "message": message, "provider": "faux", "modelId": model}), &ref)
	if ref.ID == "" || ref.Title != title {
		t.Fatalf("session_new returned %+v", ref)
	}
	return ref
}

func (r childRef) conversation(t *testing.T) int64 {
	t.Helper()
	id, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func resultRows(t *testing.T, conn *sql.DB, parent int64) []resultRow {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), `SELECT id, status, content, coalesce(answer, '') FROM submissions WHERE conversation_id = ? AND kind = 'task_result' ORDER BY rowid`, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []resultRow
	for rows.Next() {
		var r resultRow
		if err := rows.Scan(&r.id, &r.status, &r.content, &r.answer); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// waitResults waits until the parent has n results and every one is processed by the parent.
func waitResults(t *testing.T, conn *sql.DB, parent int64, n int) []resultRow {
	t.Helper()
	var rows []resultRow
	eventually(t, fmt.Sprintf("%d processed results", n), func() (bool, any) {
		rows = resultRows(t, conn, parent)
		done := len(rows) == n
		for _, r := range rows {
			done = done && r.status == "done"
		}
		return done, rows
	})
	return rows
}

type childTurnRow struct {
	id, status, reason, answer, parentRequest string
}

func childTurns(t *testing.T, conn *sql.DB, child int64) []childTurnRow {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), `SELECT id, status, coalesce(reason, ''), coalesce(answer, ''), coalesce(parent_request_id, '') FROM submissions WHERE conversation_id = ? AND kind = 'message' ORDER BY rowid`, child)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []childTurnRow
	for rows.Next() {
		var r childTurnRow
		if err := rows.Scan(&r.id, &r.status, &r.reason, &r.answer, &r.parentRequest); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func waitDoneChildTurns(t *testing.T, conn *sql.DB, child int64, n int) []childTurnRow {
	t.Helper()
	var rows []childTurnRow
	eventually(t, fmt.Sprintf("%d done child turns", n), func() (bool, any) {
		rows = childTurns(t, conn, child)
		ok := len(rows) == n
		for _, r := range rows {
			ok = ok && r.status == "done"
		}
		return ok, rows
	})
	return rows
}

func resultBody(t *testing.T, content string) map[string]any {
	t.Helper()
	var body struct {
		Result map[string]any `json:"task-result"`
	}
	if err := json.Unmarshal([]byte(content), &body); err != nil || body.Result == nil {
		t.Fatalf("result content %q: %v", content, err)
	}
	return body.Result
}

func TestUserChildSessionSendsOneResultToItsParent(t *testing.T) {
	t.Parallel()
	// Given a parent chat that creates a child session on a model that echoes its message.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Echo", "hello child", "echo")
	// When the child ends its turn.
	rows := waitResults(t, conn, parent, 1)
	// Then the parent gets one result that names the child and carries its answer.
	turns := waitDoneChildTurns(t, conn, child.conversation(t), 1)
	if rows[0].id != "result:"+turns[0].id || turns[0].parentRequest == "" {
		t.Fatalf("result %+v does not belong to turn %+v", rows[0], turns[0])
	}
	body := resultBody(t, rows[0].content)
	if body["session"] != child.ID || body["title"] != "Echo" || body["outcome"] != "done" || body["answer"] != "echo: hello child" {
		t.Fatalf("result body: %v", body)
	}
	if !strings.HasPrefix(rows[0].answer, "task-result: ") {
		t.Fatalf("parent did not process the result: %+v", rows[0])
	}
	var parentID sql.NullInt64
	if err := conn.QueryRowContext(t.Context(), `SELECT parent_id FROM conversations WHERE id = ?`, child.conversation(t)).Scan(&parentID); err != nil || parentID.Int64 != parent {
		t.Fatalf("child parent_id = %v: %v", parentID, err)
	}
	// And the child is idle in session_list.
	answer := callSessionTool(t, d, parent, "session_list", map[string]any{})
	if !strings.Contains(answer, `"state":"idle"`) || !strings.Contains(answer, child.ID) {
		t.Fatalf("session_list: %s", answer)
	}
	t.Logf("result=%s answer=%q", rows[0].id, rows[0].answer)
}

func TestUserChildSessionSendRunsAgainAndSendsAnotherResult(t *testing.T) {
	t.Parallel()
	// Given a finished child session whose first result the parent processed.
	dir := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, dir, 42)
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	f.message(1, 42, 7, childJSON(t, map[string]any{"title": "Echo", "message": "first", "provider": "faux", "modelId": "echo"}))
	created := f.next(t).Text
	var child childRef
	toolJSON(t, created, &child)
	if got := f.next(t).Text; !strings.HasPrefix(got, "task-result: ") || !strings.Contains(got, "echo: first") {
		t.Fatalf("first result in the channel: %q", got)
	}
	var parent int64
	if err := conn.QueryRowContext(t.Context(), `SELECT parent_id FROM conversations WHERE id = ?`, child.conversation(t)).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	// When the parent sends another message to the same child.
	f.message(2, 42, 7, childJSON(t, map[string]any{"tool": "session_send", "id": child.ID, "message": "second"}))
	f.next(t)
	// Then the child keeps its context, answers again, and sends a second result with another ID.
	if got := f.next(t).Text; !strings.Contains(got, "echo: second") {
		t.Fatalf("second result in the channel: %q", got)
	}
	rows := waitResults(t, conn, parent, 2)
	if rows[0].id == rows[1].id {
		t.Fatalf("results share an ID: %+v", rows)
	}
	waitDoneChildTurns(t, conn, child.conversation(t), 2)
}

func TestUserSteeredChildInputSendsNoSecondResult(t *testing.T) {
	t.Parallel()
	// Given a child session that runs a gated shell.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Steer", taskGate(t, dir, "steer"), "bash")
	waitTaskFile(t, filepath.Join(dir, "steer.pid"))
	// When the parent steers a second message into the running turn.
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo steered", "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "steer")
	// Then the shared run sends one result, which is the result of the run starter.
	waitDoneChildTurns(t, conn, child.conversation(t), 2)
	rows := waitResults(t, conn, parent, 1)
	turns := childTurns(t, conn, child.conversation(t))
	if rows[0].id != "result:"+turns[0].id {
		t.Fatalf("result %q is not the result of the starter %q", rows[0].id, turns[0].id)
	}
	// And a later turn gives the second result, so the steer did not hold one back.
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo later"}), &ref)
	rows = waitResults(t, conn, parent, 2)
	if turns = childTurns(t, conn, child.conversation(t)); len(turns) != 3 || rows[1].id != "result:"+turns[2].id {
		t.Fatalf("second result %q, turns %+v", rows[1].id, turns)
	}
}

func TestUserQueuedChildInputsEachSendAResult(t *testing.T) {
	t.Parallel()
	// Given a child session that runs a gated shell.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Queue", taskGate(t, dir, "queue"), "bash")
	waitTaskFile(t, filepath.Join(dir, "queue.pid"))
	// When the parent queues a second message behind the run.
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo queued"}), &ref)
	if ref.State != "running" {
		t.Fatalf("child state: %+v", ref)
	}
	releaseResultGate(t, dir, "queue")
	// Then each input gets its own run and its own result.
	waitDoneChildTurns(t, conn, child.conversation(t), 2)
	rows := waitResults(t, conn, parent, 2)
	if got := resultBody(t, rows[1].content)["answer"]; got != "bash: queued" {
		t.Fatalf("second answer: %v", got)
	}
}

func TestUserStoppedChildSessionSendsNoResultAndStaysUsable(t *testing.T) {
	t.Parallel()
	// Given a child session that runs a gated shell and has one queued input.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Stop", taskGate(t, dir, "stop"), "bash")
	waitTaskFile(t, filepath.Join(dir, "stop.pid"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo never"}), &ref)
	// When the parent stops the child.
	toolJSON(t, callSessionTool(t, d, parent, "session_stop", map[string]any{"id": child.ID}), &ref)
	// Then both inputs end, the shell exits, and no result arrives.
	eventually(t, "child stopped", func() (bool, any) {
		turns := childTurns(t, conn, child.conversation(t))
		ok := len(turns) == 2
		for _, r := range turns {
			ok = ok && r.status == "unanswered"
		}
		return ok, turns
	})
	waitTaskPIDGone(t, filepath.Join(dir, "stop.pid"))
	if rows := resultRows(t, conn, parent); len(rows) != 0 {
		t.Fatalf("a stopped child sent results: %+v", rows)
	}
	// And the child accepts a new message: stop then send runs and gives a result.
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo again"}), &ref)
	rows := waitResults(t, conn, parent, 1)
	if got := resultBody(t, rows[0].content)["answer"]; got != "bash: again" {
		t.Fatalf("answer after stop: %v", got)
	}
}

func TestUserRestartInterruptsRunningChildOnceAndNeverReruns(t *testing.T) {
	t.Parallel()
	// Given a child session that records each start of its shell before it waits at a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	counter := filepath.Join(dir, "counter")
	child := spawnChild(t, d, parent, "Interrupted", "printf 'run\\n' >> '"+counter+"'; "+taskGate(t, dir, "restart"), "bash")
	waitTaskFile(t, filepath.Join(dir, "restart.pid"))
	// When the control process and the engine restart on the same data.
	d.stop()
	d = startDex(t, dir, "")
	// Then the parent gets one interrupted result, and the shell does not run again.
	rows := waitResults(t, conn, parent, 1)
	body := resultBody(t, rows[0].content)
	if body["outcome"] != "interrupted" || body["session"] != child.ID {
		t.Fatalf("interrupted result: %v", body)
	}
	if got := waitTaskFile(t, counter); got != "run\n" {
		t.Fatalf("shell ran again: %q", got)
	}
	// And a second restart sends no further result.
	d.stop()
	d = startDex(t, dir, "")
	d.mainChat(t)
	waitResults(t, conn, parent, 1)
	if got := waitTaskFile(t, counter); got != "run\n" {
		t.Fatalf("shell ran again after the second restart: %q", got)
	}
}

func TestUserRestartWithdrawsQueuedChildInputsAndNamesThem(t *testing.T) {
	t.Parallel()
	// Given a child session with one running turn and two queued turns.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Backlog", taskGate(t, dir, "backlog"), "bash")
	waitTaskFile(t, filepath.Join(dir, "backlog.pid"))
	var ref childRef
	for _, message := range []string{"echo queued-one", "echo queued-two"} {
		toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": message}), &ref)
	}
	// When dex restarts.
	d.stop()
	d = startDex(t, dir, "")
	// Then one result reports the interruption and the two withdrawn inputs.
	rows := waitResults(t, conn, parent, 1)
	body := resultBody(t, rows[0].content)
	if body["outcome"] != "interrupted" || body["withdrawn"] != float64(2) {
		t.Fatalf("result after restart: %v", body)
	}
	turns := childTurns(t, conn, child.conversation(t))
	if len(turns) != 3 {
		t.Fatalf("child turns: %+v", turns)
	}
	for _, turn := range turns {
		if turn.status != "unanswered" {
			t.Fatalf("turn did not end: %+v", turns)
		}
	}
	// And the child still takes a new message.
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo back"}), &ref)
	if rows := waitResults(t, conn, parent, 2); resultBody(t, rows[1].content)["answer"] != "bash: back" {
		t.Fatalf("answer after restart: %+v", rows[1])
	}
}

func TestUserOtherConversationsCannotSendToOrStopAChild(t *testing.T) {
	t.Parallel()
	// Given a child of one chat and another chat of the same bot.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Owned", "hello", "echo")
	waitResults(t, conn, parent, 1)
	d.setModel(t, "echo")
	other := d.newChat(t, "other")
	d.watch(t, other).until(t, "other answer", answeredWith("echo: other"))
	// When the other chat tries to send to or stop the child, the child is not found.
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"session_send", map[string]any{"id": child.ID, "message": "intrude"}},
		{"session_stop", map[string]any{"id": child.ID}},
	} {
		if got := callSessionTool(t, d, other, call.tool, call.args); !strings.HasPrefix(got, "spawn error") || !strings.Contains(got, "not found") {
			t.Fatalf("%s from another chat: %s", call.tool, got)
		}
	}
	if turns := childTurns(t, conn, child.conversation(t)); len(turns) != 1 {
		t.Fatalf("an outsider added a turn: %+v", turns)
	}
	// And a child has no session tools: its own call of session_new fails and creates nothing.
	nested := spawnChild(t, d, parent, "Nested", childJSON(t, map[string]any{"title": "grandchild", "message": "hi"}), "spawn")
	rows := waitResults(t, conn, parent, 2)
	if got := resultBody(t, rows[1].content)["answer"].(string); !strings.HasPrefix(got, "spawn error") {
		t.Fatalf("child called a session tool: %s", got)
	}
	var grandchildren int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE parent_id = ?`, nested.conversation(t)).Scan(&grandchildren); err != nil || grandchildren != 0 {
		t.Fatalf("grandchildren: %d %v", grandchildren, err)
	}
}

func TestUserLegacyTaskConversationsWorkAsChildSessions(t *testing.T) {
	t.Parallel()
	// Given the data of a dex version with background tasks, and a parent chat on the spawn model.
	dir := legacyData(t)
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	spawn := map[string]string{"provider": "faux", "modelId": "spawn", "thinkingLevel": "off"}
	for _, id := range []int64{1, 2} {
		d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", id), spawn, nil)
	}
	d.setModel(t, "spawn")
	// When the parent sends a message to the migrated task "Ended".
	var ref childRef
	toolJSON(t, callSessionTool(t, d, 1, "session_send", map[string]any{"id": "2", "message": "again"}), &ref)
	// Then the old conversation runs it, and the child has no session tools.
	eventually(t, "answer of the old task", func() (bool, any) {
		turns := childTurns(t, conn, 2)
		last := turns[len(turns)-1]
		return last.status != "pending" && last.parentRequest != "", turns
	})
	toolJSON(t, callSessionTool(t, d, 1, "session_send", map[string]any{"id": "2", "message": childJSON(t, map[string]any{"title": "x", "message": "y"})}), &ref)
	eventually(t, "second answer of the old task", func() (bool, any) {
		turns := childTurns(t, conn, 2)
		return len(turns) >= 2 && turns[len(turns)-1].status == "done" && strings.HasPrefix(turns[len(turns)-1].answer, "spawn error"), turns
	})
	// And the task that was running at the upgrade has one interrupted result.
	var interrupted int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE conversation_id = 1 AND kind = 'task_result' AND id LIKE 'result:%' AND content LIKE '%interrupted%'`).Scan(&interrupted); err != nil || interrupted != 1 {
		t.Fatalf("interrupted results: %d %v", interrupted, err)
	}
}

type readTurn struct {
	Turn   string   `json:"turn"`
	Input  string   `json:"input"`
	Answer string   `json:"answer"`
	Cut    []string `json:"cut"`
}

type readPage struct {
	Turns      []readTurn `json:"turns"`
	Next       string     `json:"next"`
	Text       string     `json:"text"`
	NextOffset int        `json:"nextOffset"`
	HasMore    bool       `json:"hasMore"`
}

func readSession(t *testing.T, d *dex, caller int64, args map[string]any) readPage {
	t.Helper()
	var page readPage
	toolJSON(t, callSessionTool(t, d, caller, "session_read", args), &page)
	return page
}

func TestUserSessionReadReturnsOnlyInputsAndAnswers(t *testing.T) {
	t.Parallel()
	// Given a Chat with three turns, one of them long, the main chat, a chat of another bot, and a dream.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	chat := d.newChat(t, "one")
	for _, text := range []string{"two", "three"} {
		d.send(t, chat, text)
		d.watch(t, chat).until(t, "answer "+text, answeredWith("echo: "+text))
	}
	long := strings.Repeat("x", 5000)
	d.send(t, chat, long)
	d.watch(t, chat).until(t, "long answer", answeredWith("echo: xxxx"))
	main := d.mainChat(t)
	d.send(t, main, "main only")
	d.watch(t, main).until(t, "main answer", answeredWith("echo: main only"))
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &other, http.StatusCreated)
	var otherMain chatInfo
	d.must(t, http.MethodGet, "/api/agents/"+other.ID+"/chats/main", nil, &otherMain)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", other.ID, otherMain.ID), map[string]string{"id": uuid.NewV7().String(), "text": "other bot secret"}, nil, http.StatusAccepted)
	d.watchPath(t, fmt.Sprintf("/api/agents/%s/chats/%d/events", other.ID, otherMain.ID)).until(t, "other answer", answeredWith("echo: other bot secret"))
	dream := sessionFixture(t, d, "dream:private", 1)
	if _, err := sessionDB(t, d).ExecContext(t.Context(), "INSERT INTO submissions (id, conversation_id, content, attachments, kind, status, created_at) VALUES ('dream-turn', ?, 'dream input', 'null', 'message', 'done', 2)", dream); err != nil {
		t.Fatal(err)
	}
	reader := d.newSession(t).ID
	// When another Chat reads it by ID.
	chatID := fmt.Sprint(chat)
	page := readSession(t, d, reader, map[string]any{"id": chatID, "limit": 2})
	// Then it gets the newest turns first with their inputs and answers, cut at 4000 characters.
	if len(page.Turns) != 2 || page.Next == "" {
		t.Fatalf("first page: %+v", page)
	}
	first := page.Turns[0]
	if len(first.Input) != 4000 || len(first.Answer) != 4000 || !slices.Equal(first.Cut, []string{"input", "answer"}) {
		t.Fatalf("long turn: input=%d answer=%d cut=%v", len(first.Input), len(first.Answer), first.Cut)
	}
	if page.Turns[1].Input != "three" || page.Turns[1].Answer != "echo: three" {
		t.Fatalf("second turn: %+v", page.Turns[1])
	}
	// And before pages back to the older turns.
	older := readSession(t, d, reader, map[string]any{"id": chatID, "before": page.Next, "limit": 5})
	if len(older.Turns) != 2 || older.Turns[0].Input != "two" || older.Turns[1].Input != "one" || older.Next != "" {
		t.Fatalf("older page: %+v", older)
	}
	// And turn with offset reads the rest of a cut text.
	rest := readSession(t, d, reader, map[string]any{"id": chatID, "turn": first.Turn, "field": "input", "offset": 4000})
	if rest.Text != strings.Repeat("x", 1000) || rest.HasMore {
		t.Fatalf("rest of input: %d %v", len(rest.Text), rest.HasMore)
	}
	rest = readSession(t, d, reader, map[string]any{"id": chatID, "turn": first.Turn, "offset": 4000})
	if len(rest.Text) != 1006 {
		t.Fatalf("rest of answer: %d", len(rest.Text))
	}
	// And Main of the same bot is readable too, but only inputs and answers show up.
	if got := readSession(t, d, reader, map[string]any{"id": fmt.Sprint(main)}); len(got.Turns) != 1 || got.Turns[0].Input != "main only" {
		t.Fatalf("main: %+v", got)
	}
	// And a session of another bot or a dream is not found.
	for _, id := range []int64{otherMain.ID, dream} {
		if got := callSessionTool(t, d, reader, "session_read", map[string]any{"id": fmt.Sprint(id)}); !strings.HasPrefix(got, "spawn error") || !strings.Contains(got, "not found") {
			t.Fatalf("read of %d: %s", id, got)
		}
	}
}

type searchMatch struct {
	Session string `json:"session"`
	Title   string `json:"title"`
	Turn    string `json:"turn"`
	Field   string `json:"field"`
	Snippet string `json:"snippet"`
}

func searchSessions(t *testing.T, d *dex, caller int64, args map[string]any) []searchMatch {
	t.Helper()
	var out struct {
		Matches []searchMatch `json:"matches"`
	}
	toolJSON(t, callSessionTool(t, d, caller, "session_search", args), &out)
	// The search call itself is a turn of the caller and contains the query.
	return slices.DeleteFunc(out.Matches, func(m searchMatch) bool { return m.Session == fmt.Sprint(caller) })
}

func TestUserSessionSearchFindsInputsAndAnswers(t *testing.T) {
	t.Parallel()
	// Given sessions with a Chinese phrase, mixed-case English, a child result, and another bot.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "echo")
	notes := d.newChat(t, "数据库迁移方案已经确定")
	d.watch(t, notes).until(t, "answer", answeredWith("echo: 数据库迁移方案已经确定"))
	english := d.newChat(t, "Quarterly Report draft for Alpha")
	d.watch(t, english).until(t, "answer", answeredWith("echo: Quarterly Report draft for Alpha"))
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &other, http.StatusCreated)
	var otherMain chatInfo
	d.must(t, http.MethodGet, "/api/agents/"+other.ID+"/chats/main", nil, &otherMain)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", other.ID, otherMain.ID), map[string]string{"id": uuid.NewV7().String(), "text": "zebra quarterly"}, nil, http.StatusAccepted)
	d.watchPath(t, fmt.Sprintf("/api/agents/%s/chats/%d/events", other.ID, otherMain.ID)).until(t, "other answer", answeredWith("echo: zebra quarterly"))
	searcher := newParent(t, d)
	spawnChild(t, d, searcher, "Finder", "child-only needle", "echo")
	waitResults(t, conn, searcher, 1)
	// When a Chat searches, a Chinese phrase matches inside a sentence.
	if got := searchSessions(t, d, searcher, map[string]any{"query": "迁移方案"}); len(got) != 1 || got[0].Session != fmt.Sprint(notes) || !strings.Contains(got[0].Snippet, "迁移方案") {
		t.Fatalf("chinese: %+v", got)
	}
	// And English ignores ASCII case, and every word must match.
	if got := searchSessions(t, d, searcher, map[string]any{"query": "QUARTERLY report"}); len(got) != 1 || got[0].Session != fmt.Sprint(english) {
		t.Fatalf("english: %+v", got)
	}
	if got := searchSessions(t, d, searcher, map[string]any{"query": "quarterly missing"}); len(got) != 0 {
		t.Fatalf("a missing word still matched: %+v", got)
	}
	// And results of children are not searched, but child inputs and answers are.
	got := searchSessions(t, d, searcher, map[string]any{"query": "needle"})
	if len(got) != 1 || got[0].Title != "Finder" {
		t.Fatalf("child: %+v", got)
	}
	if got := searchSessions(t, d, searcher, map[string]any{"query": "task-result"}); len(got) != 0 {
		t.Fatalf("a result matched: %+v", got)
	}
	// And the session argument limits the search, and another bot has no hits.
	if got := searchSessions(t, d, searcher, map[string]any{"query": "echo", "session": fmt.Sprint(notes)}); len(got) != 1 || got[0].Session != fmt.Sprint(notes) {
		t.Fatalf("session filter: %+v", got)
	}
	if got := searchSessions(t, d, searcher, map[string]any{"query": "zebra"}); len(got) != 0 {
		t.Fatalf("another bot leaked: %+v", got)
	}
}

func childCwd(t *testing.T, conn *sql.DB, child int64) string {
	t.Helper()
	var cwd string
	if err := conn.QueryRowContext(t.Context(), `SELECT cwd FROM conversations WHERE id = ?`, child).Scan(&cwd); err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestUserChildSessionRunsInTheGivenProject(t *testing.T) {
	t.Parallel()
	// Given a parent in the agent workspace and a project with a skill that only the project has.
	data := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "project-check", "description: Project-only check")
	d := startDex(t, data, "")
	conn := taskResultDB(t, data)
	parent := newParent(t, d)
	// When the parent gives the project as cwd to two children.
	var ref childRef
	d.setModel(t, "spawn")
	toolJSON(t, callSessionTool(t, d, parent, "session_new", map[string]any{"title": "Skills", "message": "skills", "cwd": project, "provider": "faux", "modelId": "section"}), &ref)
	toolJSON(t, callSessionTool(t, d, parent, "session_new", map[string]any{"title": "Shell", "message": "pwd > child-cwd", "cwd": project, "provider": "faux", "modelId": "bash"}), &childRef{})
	rows := waitResults(t, conn, parent, 2)
	// Then the child cwd is the project, its shell runs there, and it finds the project skill.
	if got := childCwd(t, conn, ref.conversation(t)); got != project {
		t.Fatalf("child cwd = %q, want %q", got, project)
	}
	// The children end in any order, so find the result by its session.
	i := slices.IndexFunc(rows, func(r resultRow) bool { return resultBody(t, r.content)["session"] == ref.ID })
	if i < 0 {
		t.Fatalf("no result of session %s: %+v", ref.ID, rows)
	}
	if got := resultBody(t, rows[i].content)["answer"].(string); !strings.Contains(got, "Project-only check") {
		t.Fatalf("project skill missing: %s", got)
	}
	if got := waitTaskFile(t, filepath.Join(project, "child-cwd")); strings.TrimSpace(got) != project {
		t.Fatalf("shell cwd: %q", got)
	}
	if got := childCwd(t, conn, parent); got != "" {
		t.Fatalf("the parent cwd changed: %q", got)
	}
}

func TestUserChildSessionInheritsTheParentProject(t *testing.T) {
	t.Parallel()
	// Given a Chat with a selected project.
	data := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := startDex(t, data, "")
	conn := taskResultDB(t, data)
	parent := newParent(t, d)
	d.must(t, http.MethodPut, projectURL(parent), map[string]string{"cwd": project}, nil)
	// When the Chat creates a child without cwd.
	child := spawnChild(t, d, parent, "Inherit", "pwd > inherited-cwd", "bash")
	waitResults(t, conn, parent, 1)
	// Then the child has the cwd of the parent.
	if got := childCwd(t, conn, child.conversation(t)); got != project {
		t.Fatalf("child cwd = %q, want %q", got, project)
	}
	if got := waitTaskFile(t, filepath.Join(project, "inherited-cwd")); strings.TrimSpace(got) != project {
		t.Fatalf("shell cwd: %q", got)
	}
}

func TestUserChildSessionRefusesAnInvalidDirectory(t *testing.T) {
	t.Parallel()
	// Given a parent in the agent workspace and a file that is not a directory.
	data := t.TempDir()
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDex(t, data, "")
	conn := taskResultDB(t, data)
	parent := newParent(t, d)
	// When the parent gives a missing, relative or non-directory cwd.
	for _, cwd := range []string{filepath.Join(t.TempDir(), "missing"), "relative/dir", file} {
		got := callSessionTool(t, d, parent, "session_new", map[string]any{"title": "Bad", "message": "never", "cwd": cwd, "provider": "faux", "modelId": "echo"})
		// Then the call fails and no child exists. There is no fallback to the workspace.
		if !strings.HasPrefix(got, "spawn error") {
			t.Fatalf("cwd %q: %s", cwd, got)
		}
	}
	var children int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE parent_id IS NOT NULL`).Scan(&children); err != nil || children != 0 {
		t.Fatalf("children after refused calls: %d %v", children, err)
	}
	if rows := resultRows(t, conn, parent); len(rows) != 0 {
		t.Fatalf("results: %+v", rows)
	}
}
