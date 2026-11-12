package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

type botQuestion struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Status  string   `json:"status"`
	Action  string   `json:"action"`
}

func (d *dex) createCoder(t *testing.T) apiAgent {
	t.Helper()
	var a apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Coder", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &a, http.StatusCreated)
	return a
}

func (d *dex) botNames(t *testing.T, path string) []string {
	t.Helper()
	var list struct {
		Agents []apiAgent `json:"agents"`
	}
	d.must(t, http.MethodGet, path, nil, &list)
	names := make([]string, len(list.Agents))
	for i, a := range list.Agents {
		names[i] = a.Name
	}
	return names
}

func (d *dex) setBotModel(t *testing.T, id, model string) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/agents/"+id, map[string]string{"name": "Coder", "provider": "faux", "modelId": model, "thinkingLevel": "off", "instructions": ""}, nil)
}

// askArchive makes the default bot call bot_delete in a new Chat, and returns the open question.
func askArchive(t *testing.T, d *dex, bot string) (int64, botQuestion) {
	t.Helper()
	d.setModel(t, "tool")
	chatID := d.newChat(t, "bot_delete "+taskJSON(t, map[string]string{"bot": bot, "reason": "Not used any more"}))
	var q botQuestion
	eventually(t, "archive question", func() (bool, any) {
		var c struct {
			Question *botQuestion `json:"question"`
		}
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", chatID), nil, &c)
		if c.Question != nil {
			q = *c.Question
		}
		return c.Question != nil && c.Question.Status == "open", c.Question
	})
	d.awaitIdle(t, chatID)
	return chatID, q
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
	chatID, q := askArchive(t, d, "Coder")
	d.setModel(t, "echo")
	d.answer(t, chatID, q.ID, "Archive Coder", http.StatusAccepted)
	d.watch(t, chatID).until(t, "approval reaches the model", answeredWith(`Approved. Bot "Coder" is archived.`))
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
	// Given a bot with a Telegram channel, a chat history, and an automation that is due later.
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
	d.must(t, http.MethodPut, "/api/agents/"+bot.ID+"/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	waitSignal(t, f.pollStarted, "Telegram polling starts")
	var history sessionInfo
	d.must(t, http.MethodPost, "/api/agents/"+bot.ID+"/chats", nil, &history, http.StatusCreated)
	var auto automationInfo
	d.must(t, http.MethodPost, "/api/agents/"+bot.ID+"/automations", map[string]any{
		"name": "Nightly", "prompt": "summarize", "timeZone": "UTC", "session": "new", "enabled": true,
		"schedule": map[string]any{"kind": "once", "at": time.Now().Add(time.Hour).UnixMilli()},
	}, &auto, http.StatusCreated)
	historyBefore := count(t, conn, "SELECT count(*) FROM conversations WHERE agent_id = ?", bot.ID)
	if historyBefore == 0 {
		t.Fatal("the bot has no conversation")
	}

	// When the default bot asks to archive it, the card shows the action and nothing is archived.
	chatID, q := askArchive(t, d, "coder")
	if q.Action != "archive" || len(q.Options) != 2 || q.Options[0] != "Archive Coder" || !strings.Contains(q.Text, "Not used any more") {
		t.Fatalf("question: %+v", q)
	}
	if !slices.Equal(d.botNames(t, "/api/agents"), []string{"Dex", "Coder"}) {
		t.Fatalf("bots before approval: %v", d.botNames(t, "/api/agents"))
	}

	// And the owner approves in the web card.
	d.setModel(t, "echo")
	d.answer(t, chatID, q.ID, "Archive Coder", http.StatusAccepted)
	d.watch(t, chatID).until(t, "approval reaches the model", answeredWith(`Approved. Bot "Coder" is archived.`))

	// Then the bot leaves the list, appears in the archived list, and its channel stops.
	if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex"}) {
		t.Fatalf("bots after approval: %v", got)
	}
	if got := d.botNames(t, "/api/archived-agents"); !slices.Equal(got, []string{"Coder"}) {
		t.Fatalf("archived bots: %v", got)
	}
	waitSignal(t, f.pollCanceled, "Telegram polling stops")
	if got := questionOutcome(t, conn, q.ID); got != `dex: Approved. Bot "Coder" is archived.` {
		t.Fatalf("outcome: %q", got)
	}
	status, _ := d.call(t, http.MethodGet, "/api/agents/"+bot.ID, nil)
	if status != http.StatusNotFound {
		t.Fatalf("archived bot GET = %d", status)
	}

	// And its automation does not run when it is due, though the setting stays on.
	if _, err := conn.ExecContext(t.Context(), "UPDATE automations SET next_due_at = ? WHERE id = ?", time.Now().Add(-time.Second).UnixMilli(), auto.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if n := count(t, conn, "SELECT count(*) FROM automation_runs WHERE automation_id = ?", auto.ID); n != 0 {
		t.Fatalf("archived bot ran its automation %d times", n)
	}
	if n := count(t, conn, "SELECT count(*) FROM automations WHERE id = ? AND enabled = 1", auto.ID); n != 1 {
		t.Fatal("archive changed the enabled flag of the automation")
	}

	// When the owner restores the bot, then it is listed again, the channel polls again, the
	// automation runs, and the history is kept.
	d.must(t, http.MethodDelete, "/api/agents/"+bot.ID+"/archive", nil, nil)
	if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex", "Coder"}) {
		t.Fatalf("bots after restore: %v", got)
	}
	if got := d.botNames(t, "/api/archived-agents"); len(got) != 0 {
		t.Fatalf("archived bots after restore: %v", got)
	}
	waitSignal(t, f.pollStarted, "Telegram polling restarts")
	eventually(t, "restored automation run", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM automation_runs WHERE automation_id = ?", auto.ID)
		return n == 1, n
	})
	if n := count(t, conn, "SELECT count(*) FROM conversations WHERE agent_id = ?", bot.ID); n < historyBefore {
		t.Fatalf("history: %d before, %d after", historyBefore, n)
	}
}

func TestOwnerAnswersThatDoNotApproveKeepTheBot(t *testing.T) {
	t.Parallel()
	// Given a question that asks to archive a bot.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.createCoder(t)
	conn := sessionDB(t, d)
	for _, answer := range []string{"Cancel", "archive coder", "yes", "Archive Coder please", "Archive  Coder", "ArchiveCoder"} {
		chatID, q := askArchive(t, d, "Coder")
		// When the owner answers with anything but the approve text.
		d.setModel(t, "echo")
		d.answer(t, chatID, q.ID, answer, http.StatusAccepted)
		// Then the model sees the refusal and the bot stays.
		d.watch(t, chatID).until(t, "refusal reaches the model", answeredWith(`Not approved. Bot "Coder" is not archived.`))
		if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex", "Coder"}) {
			t.Fatalf("answer %q archived a bot: %v", answer, got)
		}
		if got := questionOutcome(t, conn, q.ID); !strings.HasPrefix(got, "dex: Not approved.") {
			t.Fatalf("answer %q outcome: %q", answer, got)
		}
	}
}

func TestOwnerApprovalIgnoresSurroundingWhitespaceOverHTTP(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{"Archive Coder ", "\nArchive Coder", "Archive Coder\u00a0"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			// Given a question that asks to archive a bot.
			d := startDex(t, t.TempDir(), "")
			d.createCoder(t)
			chatID, q := askArchive(t, d, "Coder")
			// When the owner approves with surrounding whitespace.
			d.setModel(t, "echo")
			d.answer(t, chatID, q.ID, answer, http.StatusAccepted)
			// Then the bot is archived.
			d.watch(t, chatID).until(t, "approval reaches the model", answeredWith(`Approved. Bot "Coder" is archived.`))
			if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex"}) {
				t.Fatalf("answer %q did not archive the bot: %v", answer, got)
			}
		})
	}
}

func TestOwnerRetryOfApprovalArchivesOnce(t *testing.T) {
	t.Parallel()
	// Given an approved question.
	d := startDex(t, t.TempDir(), "")
	bot := d.createCoder(t)
	chatID, q := askArchive(t, d, "Coder")
	d.setModel(t, "echo")
	answerID := d.answer(t, chatID, q.ID, "Archive Coder", http.StatusAccepted)
	d.watch(t, chatID).until(t, "approval reaches the model", answeredWith(`Approved. Bot "Coder" is archived.`))
	d.must(t, http.MethodDelete, "/api/agents/"+bot.ID+"/archive", nil, nil)

	// When the client sends the same approval again, then the bot stays restored.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID),
		map[string]string{"id": answerID, "text": "Archive Coder", "answersQuestionId": q.ID}, nil, http.StatusAccepted)
	time.Sleep(300 * time.Millisecond)
	if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex", "Coder"}) {
		t.Fatalf("the retry archived the bot again: %v", got)
	}
}

func TestBotArchiveRefusesDefaultSelfAndBusyBots(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	coder := d.createCoder(t)
	conn := sessionDB(t, d)
	// refused waits for the bot_delete call to finish with an error that has the reason, and
	// checks that no action question exists.
	refused := func(what, agent string, chatID int64, reason string) {
		t.Helper()
		d.watchPath(t, fmt.Sprintf("/api/agents/%s/chats/%d/events", agent, chatID)).until(t, what+" refused", func(tr *transcript) bool {
			for _, it := range tr.items {
				if it.Kind == "tool" && it.Name == "bot_delete" && it.Status != "" && it.Status != "running" {
					return !tr.running && strings.Contains(it.Output, reason)
				}
			}
			return false
		})
		if n := count(t, conn, "SELECT count(*) FROM questions WHERE action IS NOT NULL"); n != 0 {
			t.Fatalf("%s: a question was stored", what)
		}
	}

	// Another bot asks to archive the default bot, and to archive itself; both are refused.
	d.setBotModel(t, coder.ID, "tool")
	ask := func(target string, id string) int64 {
		var c sessionInfo
		d.must(t, http.MethodPost, "/api/agents/"+coder.ID+"/chats", nil, &c, http.StatusCreated)
		d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", coder.ID, c.ID),
			map[string]string{"id": id, "text": "bot_delete " + taskJSON(t, map[string]string{"bot": target, "reason": "test"})}, nil, http.StatusAccepted)
		return c.ID
	}
	refused("default bot", coder.ID, ask("default", "018f0000-0000-7000-8000-000000000001"), "default bot")
	refused("self", coder.ID, ask("Coder", "018f0000-0000-7000-8000-000000000003"), "itself")

	// A bot with a running session is refused.
	d.setBotModel(t, coder.ID, "bash")
	var busy sessionInfo
	d.must(t, http.MethodPost, "/api/agents/"+coder.ID+"/chats", nil, &busy, http.StatusCreated)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", coder.ID, busy.ID),
		map[string]string{"id": "018f0000-0000-7000-8000-000000000002", "text": "sleep 4"}, nil, http.StatusAccepted)
	eventually(t, "busy session", func() (bool, any) {
		var c chatInfo
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/chats/%d", coder.ID, busy.ID), nil, &c)
		return c.Busy, c
	})
	d.setModel(t, "tool")
	refused("busy bot", "default", d.newChat(t, "bot_delete "+taskJSON(t, map[string]string{"bot": "Coder", "reason": "test"})), "running session")
	if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex", "Coder"}) {
		t.Fatalf("bots: %v", got)
	}
	d.must(t, http.MethodDelete, "/api/agents/"+coder.ID+"/archive", nil, nil, http.StatusNotFound)
}

func TestBotChangesNeedAUserInput(t *testing.T) {
	t.Parallel()
	// Given a bot and a default bot that takes tool calls from its own prompts.
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), AutomationCheck: 100 * time.Millisecond})
	d.setModel(t, "tool")
	conn := sessionDB(t, d)
	create := "bot_create " + taskJSON(t, map[string]string{"name": "Sneaky"})
	update := "bot_update " + taskJSON(t, map[string]string{"bot": "default", "name": "Renamed"})
	del := "bot_delete " + taskJSON(t, map[string]string{"bot": "default", "reason": "test"})

	// When an automation turn calls each changing tool, then none of them exists in that turn.
	for _, prompt := range []string{create, update, del} {
		var auto automationInfo
		d.must(t, http.MethodPost, "/api/agents/default/automations", map[string]any{
			"name": "Auto " + prompt[:10], "prompt": prompt, "timeZone": "UTC", "session": "new", "enabled": true,
			"schedule": map[string]any{"kind": "once", "at": time.Now().Add(300 * time.Millisecond).UnixMilli()},
		}, &auto, http.StatusCreated)
		eventually(t, "automation run", func() (bool, any) {
			runs := d.runs(t, auto.ID)
			return len(runs) == 1 && runs[0].ChatID != 0 && runs[0].Status != "running", runs
		})
		d.watch(t, d.runs(t, auto.ID)[0].ChatID).until(t, "tool is not available", answeredWith("is not available"))
	}
	// Then it can read bots, but no bot was created, renamed, or asked about.
	var auto automationInfo
	d.must(t, http.MethodPost, "/api/agents/default/automations", map[string]any{
		"name": "Reader", "prompt": "bot_list {}", "timeZone": "UTC", "session": "new", "enabled": true,
		"schedule": map[string]any{"kind": "once", "at": time.Now().Add(300 * time.Millisecond).UnixMilli()},
	}, &auto, http.StatusCreated)
	var run runInfo
	eventually(t, "reader run", func() (bool, any) {
		runs := d.runs(t, auto.ID)
		if len(runs) == 1 {
			run = runs[0]
		}
		return len(runs) == 1 && run.ChatID != 0, runs
	})
	d.watch(t, run.ChatID).until(t, "bot_list result", answeredWith(`"name":"Dex"`))
	if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex"}) {
		t.Fatalf("an automation changed the bots: %v", got)
	}
	if n := count(t, conn, "SELECT count(*) FROM questions WHERE action IS NOT NULL"); n != 0 {
		t.Fatalf("an automation stored %d action questions", n)
	}

	// And a child session cannot change a bot either.
	parent := newParent(t, d)
	for i, call := range []string{create, update, del} {
		spawnChild(t, d, parent, fmt.Sprintf("child %d", i), call, "tool")
		rows := waitResults(t, conn, parent, i+1)
		if got, _ := resultBody(t, rows[i].content)["answer"].(string); !strings.Contains(got, "is not available") {
			t.Fatalf("child %q answer: %q", call[:10], got)
		}
	}
	if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex"}) {
		t.Fatalf("a child session changed the bots: %v", got)
	}
	if n := count(t, conn, "SELECT count(*) FROM questions WHERE action IS NOT NULL"); n != 0 {
		t.Fatalf("a child session stored %d action questions", n)
	}
}

func TestOwnerManagesBotsThroughChat(t *testing.T) {
	t.Parallel()
	// Given a Chat with the default bot.
	d := startDex(t, t.TempDir(), "")
	chat := d.newSession(t).ID
	// When the owner asks the bot to create and change a bot, then the changes follow the web checks.
	got := taskTool(t, d, chat, "bot_create", map[string]string{"name": "Writer", "provider": "faux", "modelId": "echo", "instructions": "Write.\nBe short."})
	if !strings.Contains(got, `"name":"Writer"`) {
		t.Fatalf("create: %s", got)
	}
	if got := taskTool(t, d, chat, "bot_create", map[string]string{"name": "Bad", "provider": "missing", "modelId": "echo"}); strings.Contains(got, `"name":"Bad"`) {
		t.Fatalf("an invalid provider was accepted: %s", got)
	}
	if got := taskTool(t, d, chat, "bot_update", map[string]string{"bot": "writer", "thinkingLevel": "high"}); !strings.Contains(got, `"thinkingLevel":"high"`) || !strings.Contains(got, `"name":"Writer"`) {
		t.Fatalf("update: %s", got)
	}
	if got := taskTool(t, d, chat, "bot_get", map[string]string{"bot": "Writer"}); !strings.Contains(got, "Be short.") {
		t.Fatalf("get: %s", got)
	}
	if got := taskTool(t, d, chat, "bot_list", map[string]string{}); !strings.Contains(got, `"name":"Writer"`) || strings.Contains(got, "Be short.") {
		t.Fatalf("list: %s", got)
	}
	if names := d.botNames(t, "/api/agents"); !slices.Equal(names, []string{"Dex", "Writer"}) {
		t.Fatalf("bots: %v", names)
	}
}

func TestArchiveRoutesHideArchivedBots(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	bot := d.createCoder(t)
	// Restore of an active bot and of an unknown bot is 404.
	d.must(t, http.MethodDelete, "/api/agents/"+bot.ID+"/archive", nil, nil, http.StatusNotFound)
	d.must(t, http.MethodDelete, "/api/agents/missing/archive", nil, nil, http.StatusNotFound)
	// The owner approves the archive, then every agent route except restore is 404.
	archiveCoder(t, d)
	for _, path := range []string{"", "/chats", "/automations", "/channels/telegram"} {
		if status, _ := d.call(t, http.MethodGet, "/api/agents/"+bot.ID+path, nil); status != http.StatusNotFound {
			t.Fatalf("GET %s = %d", path, status)
		}
	}
	d.must(t, http.MethodDelete, "/api/agents/"+bot.ID+"/archive", nil, nil)
	d.must(t, http.MethodGet, "/api/agents/"+bot.ID, nil, nil)
}

// The API has no archive route. Only an approved bot_delete question archives (ADR 0016).
func TestArchiveHasNoAPIRoute(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	bot := d.createCoder(t)
	for _, id := range []string{bot.ID, "default"} {
		for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodPatch} {
			if status, _ := d.call(t, method, "/api/agents/"+id+"/archive", nil); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
				t.Fatalf("%s archive of %s = %d, want 404 or 405", method, id, status)
			}
		}
	}
	if got := d.botNames(t, "/api/agents"); !slices.Equal(got, []string{"Dex", "Coder"}) {
		t.Fatalf("bots: %v", got)
	}
}
