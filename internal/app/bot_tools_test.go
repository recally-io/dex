package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/recally-io/dex/internal/app"
)

type botQuestion struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Status  string   `json:"status"`
	Action  string   `json:"action"`
}

func (d *dex) createCoder(t *testing.T) apiBot {
	t.Helper()
	var a apiBot
	d.must(t, http.MethodPost, "/api/bots", map[string]string{"name": "Coder", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &a, http.StatusCreated)
	return a
}

func (d *dex) botNames(t *testing.T, path string) []string {
	t.Helper()
	var list struct {
		Bots []apiBot `json:"bots"`
	}
	d.must(t, http.MethodGet, path, nil, &list)
	names := make([]string, len(list.Bots))
	for i, a := range list.Bots {
		names[i] = a.Name
	}
	return names
}

// askArchive makes the default bot call bot_delete in a new Session, and returns the open question.
func askArchive(t *testing.T, d *dex, bot string) (int64, botQuestion) {
	t.Helper()
	d.setModel(t, "tool")
	sessionID := d.newSessionWithMessage(t, "bot_delete "+taskJSON(t, map[string]string{"bot": bot, "reason": "Not used any more"}))
	var q botQuestion
	eventually(t, "archive question", func() (bool, any) {
		var c struct {
			Question *botQuestion `json:"question"`
		}
		d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d", sessionID), nil, &c)
		if c.Question != nil {
			q = *c.Question
		}
		return c.Question != nil && c.Question.Status == "open", c.Question
	})
	d.awaitIdle(t, sessionID)
	return sessionID, q
}

func waitSignal(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout: %s", what)
	}
}

// archiveCoder archives the bot "Coder" the way the owner does: the default bot asks with
// bot_delete, and the owner approves in the web card.
func archiveCoder(t *testing.T, d *dex) {
	t.Helper()
	sessionID, q := askArchive(t, d, "Coder")
	d.setModel(t, "echo")
	d.answer(t, sessionID, q.ID, "Archive Coder", http.StatusAccepted)
	d.watch(t, sessionID).until(t, "approval reaches the model", answeredWith(`Approved. Bot "Coder" is archived.`))
}

func questionOutcome(t *testing.T, conn *sql.DB, id string) string {
	t.Helper()
	var outcome sql.NullString
	if err := conn.QueryRowContext(t.Context(), "SELECT outcome FROM questions WHERE id = ?", id).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	return outcome.String
}

func count(t *testing.T, conn *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOwnerArchivesAndRestoresBotWithApproval(t *testing.T) {
	t.Parallel()
	// Given a bot with a Telegram channel, a session history, and an automation that is due later.
	f := newFakeTelegram(t)
	f.pollStarted = make(chan struct{}, 4)
	f.pollCanceled = make(chan struct{}, 4)
	release := make(chan struct{})
	f.pollRelease = release
	t.Cleanup(func() { close(release) })
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, TelegramAPIURL: f.srv.URL, AutomationCheck: 100 * time.Millisecond})
	conn := sessionDB(t, d)
	bot := d.createCoder(t)
	d.must(t, http.MethodPut, "/api/bots/"+bot.ID+"/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	waitSignal(t, f.pollStarted, "Telegram polling starts")
	var history sessionInfo
	d.must(t, http.MethodPost, "/api/bots/"+bot.ID+"/sessions", nil, &history, http.StatusCreated)
	var auto automationInfo
	d.must(t, http.MethodPost, "/api/bots/"+bot.ID+"/automations", map[string]any{
		"name": "Nightly", "prompt": "summarize", "timeZone": "UTC", "session": "new", "enabled": true,
		"schedule": map[string]any{"kind": "once", "at": time.Now().Add(time.Hour).UnixMilli()},
	}, &auto, http.StatusCreated)
	historyBefore := count(t, conn, "SELECT count(*) FROM sessions WHERE bot_id = ?", bot.ID)
	if historyBefore == 0 {
		t.Fatal("the bot has no conversation")
	}

	// When the default bot asks to archive it, the card shows the action and nothing is archived.
	sessionID, q := askArchive(t, d, "coder")
	if q.Action != "archive" || len(q.Options) != 2 || q.Options[0] != "Archive Coder" || !strings.Contains(q.Text, "Not used any more") {
		t.Fatalf("question: %+v", q)
	}
	if !slices.Equal(d.botNames(t, "/api/bots"), []string{"Dex", "Coder"}) {
		t.Fatalf("bots before approval: %v", d.botNames(t, "/api/bots"))
	}

	// And the owner approves in the web card.
	d.setModel(t, "echo")
	d.answer(t, sessionID, q.ID, "Archive Coder", http.StatusAccepted)
	d.watch(t, sessionID).until(t, "approval reaches the model", answeredWith(`Approved. Bot "Coder" is archived.`))

	// Then the bot leaves the list, appears in the archived list, and its channel stops.
	if got := d.botNames(t, "/api/bots"); !slices.Equal(got, []string{"Dex"}) {
		t.Fatalf("bots after approval: %v", got)
	}
	if got := d.botNames(t, "/api/archived-bots"); !slices.Equal(got, []string{"Coder"}) {
		t.Fatalf("archived bots: %v", got)
	}
	waitSignal(t, f.pollCanceled, "Telegram polling stops")
	if got := questionOutcome(t, conn, q.ID); got != `dex: Approved. Bot "Coder" is archived.` {
		t.Fatalf("outcome: %q", got)
	}
	status, _ := d.call(t, http.MethodGet, "/api/bots/"+bot.ID, nil)
	if status != http.StatusNotFound {
		t.Fatalf("archived bot GET = %d", status)
	}

	// And its automation does not run when it is due, though the setting stays on.
	var tickAuto automationInfo
	d.must(t, http.MethodPost, "/api/bots/default/automations", map[string]any{
		"name": "Tick check", "prompt": "ok", "timeZone": "UTC", "session": "new", "enabled": true,
		"schedule": map[string]any{"kind": "once", "at": time.Now().Add(time.Hour).UnixMilli()},
	}, &tickAuto, http.StatusCreated)
	dueAt := time.Now().Add(-time.Second).UnixMilli()
	if _, err := conn.ExecContext(t.Context(), "UPDATE automations SET next_due_at = ? WHERE id IN (?, ?)", dueAt, auto.ID, tickAuto.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "automation scheduler tick runs", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM automation_runs WHERE automation_id = ?", tickAuto.ID)
		return n == 1, n
	})
	if n := count(t, conn, "SELECT count(*) FROM automation_runs WHERE automation_id = ?", auto.ID); n != 0 {
		t.Fatalf("archived bot ran its automation %d times", n)
	}
	if n := count(t, conn, "SELECT count(*) FROM automations WHERE id = ? AND enabled = 1", auto.ID); n != 1 {
		t.Fatal("archive changed the enabled flag of the automation")
	}

	// When the owner restores the bot, then it is listed again, the channel polls again, the
	// automation runs, and the history is kept.
	d.must(t, http.MethodDelete, "/api/bots/"+bot.ID+"/archive", nil, nil)
	if got := d.botNames(t, "/api/bots"); !slices.Equal(got, []string{"Dex", "Coder"}) {
		t.Fatalf("bots after restore: %v", got)
	}
	if got := d.botNames(t, "/api/archived-bots"); len(got) != 0 {
		t.Fatalf("archived bots after restore: %v", got)
	}
	waitSignal(t, f.pollStarted, "Telegram polling restarts")
	eventually(t, "restored automation run", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM automation_runs WHERE automation_id = ?", auto.ID)
		return n == 1, n
	})
	if n := count(t, conn, "SELECT count(*) FROM sessions WHERE bot_id = ?", bot.ID); n < historyBefore {
		t.Fatalf("history: %d before, %d after", historyBefore, n)
	}
}

func TestOwnerManagesBotsThroughSession(t *testing.T) {
	t.Parallel()
	// Given a Session with the default bot.
	d := startDex(t, t.TempDir(), "")
	session := d.newSession(t).ID
	// When the owner asks the bot to create and change a bot, then the changes follow the web checks.
	got := taskTool(t, d, session, "bot_create", map[string]string{"name": "Writer", "provider": "faux", "modelId": "echo", "instructions": "Write.\nBe short."})
	if !strings.Contains(got, `"name":"Writer"`) {
		t.Fatalf("create: %s", got)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "tool: ")), &created); err != nil || created.ID == "" {
		t.Fatalf("parse bot_create: %v %s", err, got)
	}
	if got := taskTool(t, d, session, "bot_create", map[string]string{"name": "Bad", "provider": "missing", "modelId": "echo"}); strings.Contains(got, `"name":"Bad"`) {
		t.Fatalf("an invalid provider was accepted: %s", got)
	}
	if got := taskTool(t, d, session, "bot_update", map[string]string{"bot": "writer", "thinkingLevel": "high"}); !strings.Contains(got, `"thinkingLevel":"high"`) || !strings.Contains(got, `"name":"Writer"`) {
		t.Fatalf("update: %s", got)
	}
	if got := taskTool(t, d, session, "bot_get", map[string]string{"bot": "Writer"}); !strings.Contains(got, "Be short.") {
		t.Fatalf("get: %s", got)
	}
	// A bot whose Main runs bash counts one runningSession in bot_list.
	if update := taskTool(t, d, session, "bot_update", map[string]string{"bot": created.ID, "provider": "faux", "modelId": "bash"}); !strings.Contains(update, `"modelId":"bash"`) {
		t.Fatalf("update model: %s", update)
	}
	var writerMain sessionInfo
	d.must(t, http.MethodGet, "/api/bots/"+created.ID+"/sessions/main", nil, &writerMain)
	gate := t.TempDir()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/%s/sessions/%d/messages", created.ID, writerMain.ID),
		map[string]string{"id": uuid.NewString(), "text": taskGate(t, gate, "main")}, nil, http.StatusAccepted)
	waitTaskFile(t, filepath.Join(gate, "main.output"))
	got = taskTool(t, d, session, "bot_list", map[string]string{})
	if err := os.WriteFile(filepath.Join(gate, "main.gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"name":"Writer"`) || strings.Contains(got, "Be short.") {
		t.Fatalf("list: %s", got)
	}
	var list struct {
		Bots []struct {
			ID      string `json:"id"`
			Running *int64 `json:"runningSessions"`
		} `json:"bots"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "tool: ")), &list); err != nil {
		t.Fatalf("parse bot_list: %v %s", err, got)
	}
	counts := map[string]int64{}
	for _, b := range list.Bots {
		if b.Running != nil {
			counts[b.ID] = *b.Running
		}
	}
	if counts[created.ID] != 1 {
		t.Fatalf("bot_list reports %d running sessions for writer (%s): %s", counts[created.ID], created.ID, got)
	}
	if names := d.botNames(t, "/api/bots"); !slices.Equal(names, []string{"Dex", "Writer"}) {
		t.Fatalf("bots: %v", names)
	}
	// The tools accept a registered Runner and report its ID on the saved bot.
	var runner apiRunner
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]string{"name": "Machine", "kind": "runner"}, &runner, http.StatusCreated)
	got = taskTool(t, d, session, "bot_create", map[string]string{"name": "Remote writer", "runnerId": runner.ID})
	var remote struct {
		RunnerID string `json:"runnerId"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "tool: ")), &remote); err != nil || remote.RunnerID != runner.ID {
		t.Fatalf("bot_create runner: %s: %v", got, err)
	}
}
