package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"
)

// The tests in this file cover ADR 0016: child sessions in another bot, and reading and search
// across bots. They drive real PiG, real SQLite files, and the faux provider. Bot A is the default
// bot. Bot B is "Coder". Each bot has a parent chat whose model is "spawn": its message is the JSON
// arguments of one session tool, as in child_sessions_test.go.

type bots struct {
	t    *testing.T
	d    *dex
	dir  string
	conn *sql.DB
	b    apiAgent
}

// botChat is a chat of one bot.
type botChat struct {
	bots  *bots
	agent string
	id    int64
}

// startBots starts dex with bot A and bot B. Each bot has its own .env with the same key CROSS_MARKER
// and another value, and B has instructions with a marker. B has the default model "section".
func startBots(t *testing.T) *bots {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "agents", "default", ".agents", ".env"), "CROSS_MARKER=from-a\n")
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Coder", "provider": "faux", "modelId": "section", "thinkingLevel": "off", "instructions": "Instruction marker of Coder"}, &b, http.StatusCreated)
	writeFile(t, filepath.Join(dir, "agents", b.ID, ".agents", ".env"), "CROSS_MARKER=from-b\n")
	if status, body := d.reload(t); status != http.StatusOK {
		t.Fatalf("reload: %d %s", status, body)
	}
	return &bots{t: t, d: d, dir: dir, conn: taskResultDB(t, dir), b: b}
}

// chat opens a chat of the bot whose own model is "spawn". The bot keeps its default model.
func (s *bots) chat(agent string) botChat {
	s.t.Helper()
	var c sessionInfo
	s.d.must(s.t, http.MethodPost, "/api/agents/"+agent+"/chats", nil, &c, http.StatusCreated)
	s.d.must(s.t, http.MethodPut, fmt.Sprintf("/api/agents/%s/chats/%d/settings", agent, c.ID), map[string]string{"provider": "faux", "modelId": "spawn", "thinkingLevel": "off"}, nil)
	return botChat{bots: s, agent: agent, id: c.ID}
}

// echoChat opens a chat of the bot on the model "echo" and waits for its first answer.
func (s *bots) echoChat(agent, text string) botChat {
	s.t.Helper()
	var c sessionInfo
	s.d.must(s.t, http.MethodPost, "/api/agents/"+agent+"/chats", nil, &c, http.StatusCreated)
	s.d.must(s.t, http.MethodPut, fmt.Sprintf("/api/agents/%s/chats/%d/settings", agent, c.ID), map[string]string{"provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, nil)
	chat := botChat{bots: s, agent: agent, id: c.ID}
	chat.send(text)
	chat.watch().until(s.t, "answer to "+text, answeredWith("echo: "+text))
	return chat
}

func (c botChat) send(text string) string {
	c.bots.t.Helper()
	id := uuid.NewV7().String()
	c.bots.d.must(c.bots.t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", c.agent, c.id), map[string]string{"id": id, "text": text}, nil, http.StatusAccepted)
	return id
}

func (c botChat) watch() *watcher {
	c.bots.t.Helper()
	return c.bots.d.watchPath(c.bots.t, fmt.Sprintf("/api/agents/%s/chats/%d/events", c.agent, c.id))
}

// call lets the chat call one session tool through the model "spawn" and returns the answer.
func (c botChat) call(tool string, args map[string]any) string {
	c.bots.t.Helper()
	args["tool"] = tool
	id := c.send(childJSON(c.bots.t, args))
	var answer string
	c.watch().until(c.bots.t, "tool answer to "+id, func(tr *transcript) bool {
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

// newChild creates a child session with session_new and decodes the answer.
func (c botChat) newChild(args map[string]any) childRef {
	c.bots.t.Helper()
	var ref childRef
	toolJSON(c.bots.t, c.call("session_new", args), &ref)
	return ref
}

func (c botChat) operate(tool string, args map[string]any) childRef {
	c.bots.t.Helper()
	var ref childRef
	toolJSON(c.bots.t, c.call(tool, args), &ref)
	return ref
}

type conversationRow struct {
	agent, model, cwd string
	parent            sql.NullInt64
}

func (s *bots) conversation(id int64) conversationRow {
	s.t.Helper()
	var r conversationRow
	if err := s.conn.QueryRowContext(s.t.Context(), `SELECT agent_id, model_id, cwd, parent_id FROM conversations WHERE id = ?`, id).Scan(&r.agent, &r.model, &r.cwd, &r.parent); err != nil {
		s.t.Fatal(err)
	}
	return r
}

func (s *bots) pending() int {
	s.t.Helper()
	var n int
	if err := s.conn.QueryRowContext(s.t.Context(), `SELECT count(*) FROM submissions WHERE status = 'pending'`).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func TestUserBotCreatesChildSessionInAnotherBot(t *testing.T) {
	t.Parallel()
	// Given bot A and bot B, with different .env values and B with its own instructions and model.
	s := startBots(t)
	parent := s.chat("default")
	// When a chat of bot A creates a child in B by name, on a model that prints its environment.
	child := parent.newChild(map[string]any{"bot": "Coder", "title": "Env", "message": `printf %s "$CROSS_MARKER"`, "provider": "faux", "modelId": "bash"})
	// Then the child belongs to B and names B.
	if child.Bot != "Coder" || child.BotID != s.b.ID {
		t.Fatalf("session_new named another bot: %+v", child)
	}
	conv := s.conversation(child.conversation(t))
	if conv.agent != s.b.ID || conv.parent.Int64 != parent.id || !conv.parent.Valid {
		t.Fatalf("child conversation: %+v", conv)
	}
	// And the child ran with the .env of B, and the result reached the conversation of A once.
	rows := waitResults(t, s.conn, parent.id, 1)
	if got := resultBody(t, rows[0].content)["answer"]; got != "bash: from-b" {
		t.Fatalf("child environment: %v", got)
	}
	turns := waitDoneChildTurns(t, s.conn, child.conversation(t), 1)
	if rows[0].id != "result:"+turns[0].id {
		t.Fatalf("result %q does not belong to turn %q", rows[0].id, turns[0].id)
	}
	// And a child in B by ID, with no model, takes the defaults of B and the instructions of B.
	second := parent.newChild(map[string]any{"bot": s.b.ID, "title": "Instructions", "message": "instructions"})
	rows = waitResults(t, s.conn, parent.id, 2)
	if got, _ := resultBody(t, rows[1].content)["answer"].(string); !strings.Contains(got, "Instruction marker of Coder") {
		t.Fatalf("child instructions: %v", got)
	}
	if conv = s.conversation(second.conversation(t)); conv.agent != s.b.ID || conv.model != "section" || conv.cwd != "" {
		t.Fatalf("child defaults: %+v", conv)
	}
	// And a child with no bot stays in A, with the settings of its parent.
	same := parent.newChild(map[string]any{"title": "Same", "message": `printf %s "$CROSS_MARKER"`, "provider": "faux", "modelId": "bash"})
	rows = waitResults(t, s.conn, parent.id, 3)
	if got := resultBody(t, rows[2].content)["answer"]; same.BotID != "default" || got != "bash: from-a" {
		t.Fatalf("child without bot: %+v %v", same, got)
	}
	// And an unknown bot fails, and creates nothing.
	if got := parent.call("session_new", map[string]any{"bot": "Nobody", "title": "Lost", "message": "hi"}); !strings.HasPrefix(got, "spawn error") || !strings.Contains(got, "no bot has") {
		t.Fatalf("unknown bot: %s", got)
	}
	if len(resultRows(t, s.conn, parent.id)) != 3 {
		t.Fatal("a failed session_new created a child")
	}
}

func TestUserParentOperatesItsChildInAnotherBot(t *testing.T) {
	t.Parallel()
	// Given a child in B that runs a gated shell, and the parent chat in A.
	s := startBots(t)
	parent := s.chat("default")
	child := parent.newChild(map[string]any{"bot": "Coder", "title": "Gated", "message": taskGate(t, s.dir, "first"), "provider": "faux", "modelId": "bash"})
	id := child.conversation(t)
	waitTaskFile(t, filepath.Join(s.dir, "first.pid"))
	// When the parent steers a message into the running turn.
	parent.operate("session_send", map[string]any{"id": child.ID, "message": "echo steered", "mode": "steer"})
	releaseResultGate(t, s.dir, "first")
	// Then the shared run sends one result, and the child stays in B.
	waitDoneChildTurns(t, s.conn, id, 2)
	waitResults(t, s.conn, parent.id, 1)
	// And a follow-up to the idle child starts a run and sends a second result.
	if ref := parent.operate("session_send", map[string]any{"id": child.ID, "message": "echo again"}); ref.Bot != "Coder" {
		t.Fatalf("session_send named another bot: %+v", ref)
	}
	rows := waitResults(t, s.conn, parent.id, 2)
	if got := resultBody(t, rows[1].content)["answer"]; got != "bash: again" {
		t.Fatalf("follow-up answer: %v", got)
	}
	// And Stop stops the run and the queued input, sends no result, and leaves the child usable.
	parent.operate("session_send", map[string]any{"id": child.ID, "message": taskGate(t, s.dir, "second")})
	waitTaskFile(t, filepath.Join(s.dir, "second.pid"))
	parent.operate("session_send", map[string]any{"id": child.ID, "message": "echo never"})
	parent.operate("session_stop", map[string]any{"id": child.ID})
	waitTaskPIDGone(t, filepath.Join(s.dir, "second.pid"))
	eventually(t, "stopped turns", func() (bool, any) {
		turns := childTurns(t, s.conn, id)
		last := turns[len(turns)-2:]
		return last[0].status == "unanswered" && last[1].status == "unanswered", turns
	})
	if rows := resultRows(t, s.conn, parent.id); len(rows) != 2 {
		t.Fatalf("a stopped child sent a result: %+v", rows)
	}
	parent.operate("session_send", map[string]any{"id": child.ID, "message": "echo after stop"})
	waitResults(t, s.conn, parent.id, 3)
	// And a fork of the child belongs to B and to the same parent.
	fork := parent.operate("session_fork", map[string]any{"id": child.ID, "message": "echo forked"})
	if conv := s.conversation(fork.conversation(t)); conv.agent != s.b.ID || conv.parent.Int64 != parent.id || fork.Bot != "Coder" {
		t.Fatalf("fork: %+v %+v", fork, conv)
	}
	rows = waitResults(t, s.conn, parent.id, 4)
	if got := resultBody(t, rows[3].content)["answer"]; got != "bash: forked" {
		t.Fatalf("fork answer: %v", got)
	}
}

func TestUserOnlyTheParentOperatesAChildInAnotherBot(t *testing.T) {
	t.Parallel()
	// Given a child in B, another chat in A, and a chat in B.
	s := startBots(t)
	parent := s.chat("default")
	child := parent.newChild(map[string]any{"bot": "Coder", "title": "Owned", "message": "hello", "provider": "faux", "modelId": "echo"})
	waitResults(t, s.conn, parent.id, 1)
	before := len(childTurns(t, s.conn, child.conversation(t)))
	// When they try to send to, stop, or fork the child, the child is not found.
	for _, caller := range []botChat{s.chat("default"), s.chat(s.b.ID)} {
		for _, call := range []struct {
			tool string
			args map[string]any
		}{
			{"session_send", map[string]any{"id": child.ID, "message": "intrude"}},
			{"session_stop", map[string]any{"id": child.ID}},
			{"session_fork", map[string]any{"id": child.ID, "message": "intrude"}},
		} {
			if got := caller.call(call.tool, call.args); !strings.HasPrefix(got, "spawn error") || !strings.Contains(got, "not found") {
				t.Fatalf("%s from a chat of %s: %s", call.tool, caller.agent, got)
			}
		}
	}
	if turns := childTurns(t, s.conn, child.conversation(t)); len(turns) != before {
		t.Fatalf("an outsider added a turn: %+v", turns)
	}
	// And a child cannot give work to a bot: it has no session tools.
	nested := parent.newChild(map[string]any{"title": "Nested", "message": childJSON(t, map[string]any{"bot": "Coder", "title": "grandchild", "message": "hi"}), "provider": "faux", "modelId": "spawn"})
	rows := waitResults(t, s.conn, parent.id, 2)
	if got, _ := resultBody(t, rows[1].content)["answer"].(string); !strings.HasPrefix(got, "spawn error") {
		t.Fatalf("a child called a session tool: %s", got)
	}
	var grandchildren int
	if err := s.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE parent_id = ?`, nested.conversation(t)).Scan(&grandchildren); err != nil || grandchildren != 0 {
		t.Fatalf("grandchildren: %d %v", grandchildren, err)
	}
}

// TestUserParentsStopChildrenInEachOthersBotsWithoutDeadlock covers the lock rule of ADR 0016:
// a settlement in B and a delivery in A, and a Stop in A and a Stop in B, run at the same time and
// finish. Run it with -race -count=50.
func TestUserParentsStopChildrenInEachOthersBotsWithoutDeadlock(t *testing.T) {
	t.Parallel()
	// Given a parent chat in each bot.
	s := startBots(t)
	parents := []botChat{s.chat("default"), s.chat(s.b.ID)}
	targets := []string{s.b.ID, "default"}
	// When each parent creates two short children in the other bot. They run while the next
	// child is created, so settlements in one bot overlap deliveries in the other.
	for n := range 2 {
		for i, parent := range parents {
			parent.newChild(map[string]any{"bot": targets[i], "title": fmt.Sprintf("Short %d", n), "message": "sleep 0.2; echo short", "provider": "faux", "modelId": "bash"})
		}
	}
	// Then every result is delivered to the conversation of its parent, and the parents process it.
	for _, parent := range parents {
		rows := waitResults(t, s.conn, parent.id, 2)
		for _, row := range rows {
			if got := resultBody(t, row.content)["answer"]; got != "bash: short" {
				t.Fatalf("answer: %v", got)
			}
		}
	}
	// And when each parent has a gated child in the other bot and both parents are stopped at once,
	for i, parent := range parents {
		name := fmt.Sprintf("gate-%d", i)
		parent.newChild(map[string]any{"bot": targets[i], "title": name, "message": taskGate(t, s.dir, name), "provider": "faux", "modelId": "bash"})
		waitTaskFile(t, filepath.Join(s.dir, name+".pid"))
	}
	var wg sync.WaitGroup
	statuses := make([]int, len(parents))
	for i, parent := range parents {
		wg.Go(func() {
			// Goroutines must not call t.Fatal: the status is checked after Wait.
			statuses[i], _ = s.d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/abort", parent.agent, parent.id), nil)
		})
	}
	wg.Wait()
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK {
		t.Fatalf("abort statuses: %v", statuses)
	}
	// Then every shell exits, every input ends, and no result follows a stop.
	for i := range parents {
		waitTaskPIDGone(t, filepath.Join(s.dir, fmt.Sprintf("gate-%d.pid", i)))
	}
	eventually(t, "no pending input", func() (bool, any) { return s.pending() == 0, s.pending() })
	for _, parent := range parents {
		if rows := resultRows(t, s.conn, parent.id); len(rows) != 2 {
			t.Fatalf("results of chat %d after Stop: %d", parent.id, len(rows))
		}
	}
}

func TestUserRestartInterruptsAChildInAnotherBotOnce(t *testing.T) {
	t.Parallel()
	// Given a child in B that records each start of its shell before it waits at a gate.
	s := startBots(t)
	parent := s.chat("default")
	counter := filepath.Join(s.dir, "counter")
	child := parent.newChild(map[string]any{"bot": "Coder", "title": "Interrupted", "message": "printf 'run\\n' >> '" + counter + "'; " + taskGate(t, s.dir, "restart"), "provider": "faux", "modelId": "bash"})
	waitTaskFile(t, filepath.Join(s.dir, "restart.pid"))
	// When dex restarts on the same data.
	s.d.stop()
	d := startDex(t, s.dir, "")
	// Then the conversation of A gets one interrupted result, and the shell does not run again.
	rows := waitResults(t, s.conn, parent.id, 1)
	body := resultBody(t, rows[0].content)
	if body["outcome"] != "interrupted" || body["session"] != child.ID {
		t.Fatalf("interrupted result: %v", body)
	}
	if got := waitTaskFile(t, counter); got != "run\n" {
		t.Fatalf("shell ran again: %q", got)
	}
	// And a second restart sends nothing more.
	d.stop()
	d = startDex(t, s.dir, "")
	d.mainChat(t)
	waitResults(t, s.conn, parent.id, 1)
	if got := waitTaskFile(t, counter); got != "run\n" {
		t.Fatalf("shell ran again after the second restart: %q", got)
	}
}

type botMatch struct {
	Session string `json:"session"`
	Title   string `json:"title"`
	Bot     string `json:"bot"`
	BotID   string `json:"botId"`
}

func (c botChat) search(args map[string]any) []botMatch {
	c.bots.t.Helper()
	var out struct {
		Matches []botMatch `json:"matches"`
	}
	toolJSON(c.bots.t, c.call("session_search", args), &out)
	// The search call is a turn of this chat and contains the query.
	return slices.DeleteFunc(out.Matches, func(m botMatch) bool { return m.Session == fmt.Sprint(c.id) })
}

func sessionIDs(matches []botMatch) []string {
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i] = m.Session
	}
	slices.Sort(ids)
	return ids
}

func TestUserSessionSearchCoversOneBotOrAllBots(t *testing.T) {
	t.Parallel()
	// Given a session with the word "zebra" in each bot, and a dream of bot A with the same word.
	s := startBots(t)
	inA := s.echoChat("default", "zebra in bot a")
	inB := s.echoChat(s.b.ID, "zebra in bot b")
	dream := sessionFixture(t, s.d, "dream:private", 1)
	if _, err := sessionDB(t, s.d).ExecContext(t.Context(), "INSERT INTO submissions (id, conversation_id, content, attachments, kind, status, created_at) VALUES ('dream-zebra', ?, 'dream zebra', 'null', 'message', 'done', 2)", dream); err != nil {
		t.Fatal(err)
	}
	searcher := s.chat("default")
	want := func(what string, got []botMatch, chats ...botChat) {
		t.Helper()
		ids := make([]string, len(chats))
		for i, c := range chats {
			ids[i] = fmt.Sprint(c.id)
		}
		slices.Sort(ids)
		if !slices.Equal(sessionIDs(got), ids) {
			t.Fatalf("%s: got %+v, want sessions %v", what, got, ids)
		}
	}
	// When it searches with no bot, only the current bot answers.
	got := searcher.search(map[string]any{"query": "zebra"})
	want("empty bot", got, inA)
	if got[0].Bot != "Dex" || got[0].BotID != "default" {
		t.Fatalf("match does not name its bot: %+v", got[0])
	}
	// And bot all covers every bot, and each match names its bot. A dream stays hidden.
	got = searcher.search(map[string]any{"query": "zebra", "bot": "all"})
	want("all bots", got, inA, inB)
	for _, m := range got {
		if m.Session == fmt.Sprint(inB.id) && (m.Bot != "Coder" || m.BotID != s.b.ID) {
			t.Fatalf("match does not name bot B: %+v", m)
		}
	}
	// And a bot by name or by ID covers that bot only.
	want("bot by name", searcher.search(map[string]any{"query": "zebra", "bot": "coder"}), inB)
	want("bot by ID", searcher.search(map[string]any{"query": "zebra", "bot": s.b.ID}), inB)
	// And a session of another bot is found only when the search names that bot.
	want("session of another bot, default bot", searcher.search(map[string]any{"query": "zebra", "session": fmt.Sprint(inB.id)}))
	want("session of another bot, named bot", searcher.search(map[string]any{"query": "zebra", "session": fmt.Sprint(inB.id), "bot": "Coder"}), inB)
	// And an unknown bot, and a name that two bots share, fail with a message.
	if got := searcher.call("session_search", map[string]any{"query": "zebra", "bot": "Nobody"}); !strings.HasPrefix(got, "spawn error") || !strings.Contains(got, "no bot has") {
		t.Fatalf("unknown bot: %s", got)
	}
	var twin apiAgent
	s.d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Coder", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &twin, http.StatusCreated)
	if got := searcher.call("session_search", map[string]any{"query": "zebra", "bot": "Coder"}); !strings.Contains(got, "2 bots have the name") || !strings.Contains(got, twin.ID) || !strings.Contains(got, s.b.ID) {
		t.Fatalf("ambiguous bot: %s", got)
	}
	want("ambiguous name by ID", searcher.search(map[string]any{"query": "zebra", "bot": twin.ID}))
}

func TestUserSessionListCoversOneBotOrAllBots(t *testing.T) {
	t.Parallel()
	// Given a parent chat in A with one child in A and one child in B, and a chat in B.
	s := startBots(t)
	parent := s.chat("default")
	inA := parent.newChild(map[string]any{"title": "Child in A", "message": "hello a", "provider": "faux", "modelId": "echo"})
	inB := parent.newChild(map[string]any{"bot": "Coder", "title": "Child in B", "message": "hello b", "provider": "faux", "modelId": "echo"})
	waitResults(t, s.conn, parent.id, 2)
	other := s.echoChat(s.b.ID, "chat of b")
	list := func(args map[string]any) map[string]string {
		t.Helper()
		var out struct {
			Sessions []struct {
				ID    string `json:"id"`
				Bot   string `json:"bot"`
				State string `json:"state"`
			} `json:"sessions"`
		}
		toolJSON(t, parent.call("session_list", args), &out)
		bots := map[string]string{}
		for _, session := range out.Sessions {
			bots[session.ID] = session.Bot
		}
		return bots
	}
	// When the parent lists its children, both show with their bots, whatever the bot argument says.
	for _, args := range []map[string]any{{}, {"bot": "Coder"}, {"bot": "all"}, {"bot": "Dex"}} {
		if got := list(args); len(got) != 2 || got[inA.ID] != "Dex" || got[inB.ID] != "Coder" {
			t.Fatalf("children with %v: %v", args, got)
		}
	}
	// And scope all with a named bot lists the sessions of that bot, a chat of it too, and keeps the
	// children of the parent in the other bot.
	got := list(map[string]any{"scope": "all", "bot": "Coder"})
	if got[inB.ID] != "Coder" || got[fmt.Sprint(other.id)] != "Coder" || got[inA.ID] != "Dex" || got[fmt.Sprint(parent.id)] != "" {
		t.Fatalf("all sessions, bot B: %v", got)
	}
	// And scope all with no bot lists the sessions of this bot, and keeps the child in B but not the
	// chat of B.
	got = list(map[string]any{"scope": "all"})
	if got[inB.ID] != "Coder" || got[inA.ID] != "Dex" || got[fmt.Sprint(parent.id)] != "Dex" || got[fmt.Sprint(other.id)] != "" {
		t.Fatalf("all sessions, empty bot: %v", got)
	}
	// And scope all with all lists every session.
	if got = list(map[string]any{"scope": "all", "bot": "all"}); got[fmt.Sprint(other.id)] != "Coder" || got[fmt.Sprint(parent.id)] != "Dex" {
		t.Fatalf("all sessions, all bots: %v", got)
	}
}

func TestUserSessionReadCoversAnotherBotButNotDreams(t *testing.T) {
	t.Parallel()
	// Given a chat in B with a turn, and a dream in bot A.
	s := startBots(t)
	inB := s.echoChat(s.b.ID, "private of bot b")
	dream := sessionFixture(t, s.d, "dream:private", 1)
	reader := s.chat("default")
	// When a chat in A reads the chat in B, it gets the turns and the bot.
	var page readPage
	toolJSON(t, reader.call("session_read", map[string]any{"id": fmt.Sprint(inB.id)}), &page)
	if len(page.Turns) != 1 || page.Turns[0].Input != "private of bot b" || page.Turns[0].Answer != "echo: private of bot b" {
		t.Fatalf("read of bot B: %+v", page)
	}
	if page.Bot != "Coder" || page.BotID != s.b.ID {
		t.Fatalf("page does not name its bot: %+v", page)
	}
	// And a dream is not found.
	if got := reader.call("session_read", map[string]any{"id": fmt.Sprint(dream)}); !strings.HasPrefix(got, "spawn error") || !strings.Contains(got, "not found") {
		t.Fatalf("read of a dream: %s", got)
	}
}

func TestUserParentReadsTheFilesOfItsChildInAnotherBot(t *testing.T) {
	t.Parallel()
	// Given a file in the workspace of B, and a child in B that delivers it for a parent in A.
	s := startBots(t)
	writeFile(t, filepath.Join(s.dir, "agents", s.b.ID, "workspace", "report.markdown"), "# Report\n")
	if err := os.Chmod(filepath.Join(s.dir, "agents", s.b.ID, "workspace", "report.markdown"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := s.chat("default")
	child := parent.newChild(map[string]any{"bot": "Coder", "title": "Files", "message": `deliver_file {"path":"report.markdown"}`, "provider": "faux", "modelId": "tool"})
	waitResults(t, s.conn, parent.id, 1)
	// When the parent lists its files in bot A, the file of the child shows in the parent.
	files := listArtifacts(t, s.d, parent.id)
	if len(files) != 1 || fmt.Sprint(files[0].ConversationID) != child.ID || files[0].DisplayConversationID != parent.id {
		t.Fatalf("artifacts of the parent: %+v", files)
	}
	// And the parent reads the bytes through the API of bot A, from the home of bot B.
	if _, body := readArtifact(t, s.d, parent.id, files[0].ID); string(body) != "# Report\n" {
		t.Fatalf("file read through the parent: %q", body)
	}
	// And the file is not in the home of bot A.
	if _, err := os.Stat(filepath.Join(s.dir, "agents", "default", "artifacts", files[0].ID)); err == nil {
		t.Fatal("the file was copied into the home of bot A")
	}
	// And bot B still lists the file in the child.
	var result struct {
		Artifacts []deliveredArtifact `json:"artifacts"`
	}
	s.d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/chats/%s/artifacts", s.b.ID, child.ID), nil, &result)
	if len(result.Artifacts) != 1 || result.Artifacts[0].ID != files[0].ID {
		t.Fatalf("artifacts of the child: %+v", result.Artifacts)
	}
	// And a chat in A that is not the parent does not list it.
	if got := listArtifacts(t, s.d, s.chat("default").id); len(got) != 0 {
		t.Fatalf("artifacts of another chat: %+v", got)
	}
}
