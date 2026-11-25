package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"
)

// The tests in this file cover cross-bot sessions: child sessions in another bot, and reading and search
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

const crossMarkerCall = `bash {"command":"case $CROSS_MARKER in from-a-*) printf from-a;; from-b-*) printf from-b;; esac","env":{"CROSS_MARKER":"vault:CROSS_MARKER"}}`

// startBots starts dex with two bots that have different values for CROSS_MARKER
// and another value, and B has instructions with a marker. B has the default model "section".
func startBots(t *testing.T) *bots {
	t.Helper()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	saveVault(t, d, "default", "agent", "CROSS_MARKER", "from-a-private")
	d.setModel(t, "echo")
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Coder", "provider": "faux", "modelId": "section", "thinkingLevel": "off", "instructions": "Instruction marker of Coder"}, &b, http.StatusCreated)
	saveVault(t, d, b.ID, "agent", "CROSS_MARKER", "from-b-private")
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
	// Given bot A and bot B, with different vault values and B with its own instructions and model.
	s := startBots(t)
	parent := s.chat("default")
	// When a chat of bot A creates a child in B by name, on a model that prints its environment.
	child := parent.newChild(map[string]any{"bot": "Coder", "title": "Env", "message": crossMarkerCall, "provider": "faux", "modelId": "tool"})
	// Then the child belongs to B and names B.
	if child.Bot != "Coder" || child.BotID != s.b.ID {
		t.Fatalf("session_new named another bot: %+v", child)
	}
	conv := s.conversation(child.conversation(t))
	if conv.agent != s.b.ID || conv.parent.Int64 != parent.id || !conv.parent.Valid {
		t.Fatalf("child conversation: %+v", conv)
	}
	// And the child ran with the vault value of B, and the result reached the conversation of A once.
	rows := waitResults(t, s.conn, parent.id, 1)
	if got := resultBody(t, rows[0].content)["answer"]; got != "tool: from-b" {
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
	same := parent.newChild(map[string]any{"title": "Same", "message": crossMarkerCall, "provider": "faux", "modelId": "tool"})
	rows = waitResults(t, s.conn, parent.id, 3)
	if got := resultBody(t, rows[2].content)["answer"]; same.BotID != "default" || got != "tool: from-a" {
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

// TestUserParentsStopChildrenInEachOthersBotsWithoutDeadlock covers the cross-bot lock rule:
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

func TestUserMainStopPreservesNextTurnAndChildren(t *testing.T) {
	t.Parallel()
	// Given Main started a gated child in each bot and now has a gated turn of its own.
	s := startBots(t)
	main := botChat{bots: s, agent: "default", id: s.d.mainChat(t)}
	path := fmt.Sprintf("/api/agents/default/chats/%d", main.id)
	setModel := func(model string) {
		s.d.must(t, http.MethodPut, path+"/settings", map[string]string{"provider": "faux", "modelId": model, "thinkingLevel": "off"}, nil)
	}
	setModel("spawn")
	children := []struct {
		bot, agent, gate string
		id               int64
	}{
		{agent: "default", gate: "main-local-child"},
		{bot: "Coder", agent: s.b.ID, gate: "main-cross-child"},
	}
	for i, child := range children {
		args := map[string]any{"title": child.gate, "message": taskGate(t, s.dir, child.gate), "provider": "faux", "modelId": "bash"}
		if child.bot != "" {
			args["bot"] = child.bot
		}
		children[i].id = main.newChild(args).conversation(t)
		waitTaskFile(t, filepath.Join(s.dir, child.gate+".pid"))
	}
	setModel("bash")
	main.send(taskGate(t, s.dir, "main-turn"))
	mainPID := filepath.Join(s.dir, "main-turn.pid")
	waitTaskFile(t, mainPID)

	// When the next input is queued, Main Stop rejects a missing or foreign target.
	main.send(taskGate(t, s.dir, "main-next"))
	s.d.must(t, http.MethodPost, path+"/abort", nil, nil, http.StatusBadRequest)
	abort := func(path, target string, status int) {
		t.Helper()
		s.d.must(t, http.MethodPost, path+"/abort?runTarget="+url.QueryEscape(target), nil, nil, status)
	}
	for _, child := range children {
		target := (botChat{bots: s, agent: child.agent, id: child.id}).watch().activeRunTarget(t)
		abort(path, target, http.StatusConflict)
		if taskPIDGone(t, filepath.Join(s.dir, child.gate+".pid")) {
			t.Fatalf("foreign target stopped %s", child.gate)
		}
	}
	w := main.watch()
	first := w.activeRunTarget(t)
	abort(fmt.Sprintf("/api/agents/%s/chats/%d", s.b.ID, main.id), first, http.StatusNotFound)
	abort(path, first, http.StatusOK)
	waitTaskPIDGone(t, mainPID)

	// Then the next turn really runs, and a reconnect supplies its new target.
	waitTaskFile(t, filepath.Join(s.dir, "main-next.pid"))
	w.until(t, "successor target", func(tr *transcript) bool { return tr.running && tr.runTarget != "" && tr.runTarget != first })
	next := main.watch().activeRunTarget(t)
	if next != w.tr.runTarget {
		t.Fatalf("snapshot and live target differ: %q and %q", next, w.tr.runTarget)
	}
	abort(path, first, http.StatusConflict)
	abort(path, first, http.StatusConflict)
	releaseResultGate(t, s.dir, "main-next")
	w.until(t, "successor answer after stale Stops", answeredWith("bash: gate released"))
	for _, child := range children {
		if taskPIDGone(t, filepath.Join(s.dir, child.gate+".pid")) {
			t.Fatalf("Main Stop crossed into %s", child.gate)
		}
	}
	// A supplied but empty target must not turn into session cancellation.
	child := children[0]
	childPath := fmt.Sprintf("/api/agents/%s/chats/%d", child.agent, child.id)
	abort(childPath, "", http.StatusConflict)
	if taskPIDGone(t, filepath.Join(s.dir, child.gate+".pid")) {
		t.Fatal("empty target stopped the child")
	}
	// An explicit session cancellation still works when the target is omitted.
	s.d.must(t, http.MethodPost, childPath+"/abort", nil, nil)
	waitTaskPIDGone(t, filepath.Join(s.dir, child.gate+".pid"))
	child = children[1]
	releaseResultGate(t, s.dir, child.gate)
	(botChat{bots: s, agent: child.agent, id: child.id}).watch().until(t, "cross-bot child answer", answeredWith("bash: gate released"))
}

// crossBotChat is a session in the list of the web API. The bot fields are set only when the session
// or its parent belongs to another bot than the one in the path.
type crossBotChat struct {
	ID                int64  `json:"id"`
	ParentID          *int64 `json:"parentId"`
	AgentID           string `json:"agentId"`
	AgentName         string `json:"agentName"`
	ParentAgentID     string `json:"parentAgentId"`
	ParentAgentName   string `json:"parentAgentName"`
	StoppedWithParent *bool  `json:"stoppedWithParent"`
}

func (s *bots) webList(agent string) map[int64]crossBotChat {
	s.t.Helper()
	var list struct {
		Chats []crossBotChat `json:"chats"`
	}
	s.d.must(s.t, http.MethodGet, "/api/agents/"+agent+"/chats", nil, &list)
	out := map[int64]crossBotChat{}
	for _, c := range list.Chats {
		out[c.ID] = c
	}
	return out
}

func TestUserParentsWithdrawMixedInputInEachOthersBotsWithoutDeadlock(t *testing.T) {
	t.Parallel()
	// Given a parent chat in each bot with a gated child in the other bot.
	// Each child has a queued user input and a queued parent input.
	s := startBots(t)
	parents := []botChat{s.chat("default"), s.chat(s.b.ID)}
	targets := []string{s.b.ID, "default"}
	children := make([]int64, len(parents))
	for i, parent := range parents {
		name := fmt.Sprintf("mixed-%d", i)
		child := parent.newChild(map[string]any{"bot": targets[i], "title": name, "message": taskGate(t, s.dir, name), "provider": "faux", "modelId": "bash"})
		children[i] = child.conversation(t)
		waitTaskFile(t, filepath.Join(s.dir, name+".pid"))
		botChat{bots: s, agent: targets[i], id: children[i]}.send("echo user queued")
		parent.operate("session_send", map[string]any{"id": child.ID, "message": "echo parent queued"})
	}
	// When both parents are stopped at once, while the user writes to both children,
	var wg sync.WaitGroup
	statuses := make([]int, len(parents))
	for i, parent := range parents {
		wg.Go(func() {
			// Goroutines must not call t.Fatal: the status is checked after Wait.
			statuses[i], _ = s.d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/abort", parent.agent, parent.id), nil)
		})
		wg.Go(func() {
			_, _ = s.d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", targets[i], children[i]), map[string]string{"id": uuid.NewV7().String(), "text": "echo user later"})
		})
	}
	wg.Wait()
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK {
		t.Fatalf("abort statuses: %v", statuses)
	}
	// Then each target withdraws only the queued parent input while the child run stays active.
	for i, child := range children {
		eventually(t, fmt.Sprintf("child %d withdraws queued parent input", i), func() (bool, any) {
			turns := childTurns(t, s.conn, child)
			return len(turns) >= 3 && turns[0].status == "pending" && turns[1].status == "pending" && turns[2].status == "unanswered" && turns[2].reason == "stopped", turns
		})
	}
	// And every run ends after its gate opens.
	for i, child := range children {
		releaseResultGate(t, s.dir, fmt.Sprintf("mixed-%d", i))
		waitChildTurnsEnded(t, s.conn, child, len(childTurns(t, s.conn, child)))
	}
}

func TestUserWebSeesAChildInAnotherBotWithItsBots(t *testing.T) {
	t.Parallel()
	// Given a parent chat in A with one child in A and one child in B, and an unrelated chat in B.
	s := startBots(t)
	parent := s.chat("default")
	inA := parent.newChild(map[string]any{"title": "Child in A", "message": "hello a", "provider": "faux", "modelId": "echo"})
	inB := parent.newChild(map[string]any{"bot": "Coder", "title": "Child in B", "message": "hello b", "provider": "faux", "modelId": "echo"})
	waitResults(t, s.conn, parent.id, 2)
	other := s.echoChat(s.b.ID, "chat of b")
	childA, childB := inA.conversation(t), inB.conversation(t)
	// When the web lists the sessions of A, the child in B shows with its bot, and the child in A does not.
	listA := s.webList("default")
	if got := listA[childB]; got.AgentID != s.b.ID || got.AgentName != "Coder" || got.ParentID == nil || *got.ParentID != parent.id || got.ParentAgentID != "" {
		t.Fatalf("child in B, list of A: %+v", got)
	}
	if got, ok := listA[childA]; !ok || got.AgentID != "" || got.ParentAgentID != "" {
		t.Fatalf("child in A, list of A: %+v", got)
	}
	// And the chat of B that has no parent in A stays out of the list of A.
	if _, ok := listA[other.id]; ok {
		t.Fatal("a chat of B shows in the list of A")
	}
	// And the list of B shows the child in B with the bot of its parent.
	listB := s.webList(s.b.ID)
	if got := listB[childB]; got.AgentID != "" || got.ParentAgentID != "default" || got.ParentAgentName != "Dex" {
		t.Fatalf("child in B, list of B: %+v", got)
	}
	if _, ok := listB[childA]; ok {
		t.Fatal("the child in A shows in the list of B")
	}
	// And a single chat read in B names the bot of its parent.
	var one crossBotChat
	s.d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/chats/%d", s.b.ID, childB), nil, &one)
	if one.ParentAgentID != "default" || one.ParentAgentName != "Dex" {
		t.Fatalf("child in B, chat of B: %+v", one)
	}
	// And the link of a result in A opens the child in B: the turn names it with its bot.
	turn := waitDoneChildTurns(t, s.conn, childB, 1)[0].id
	var opened crossBotChat
	s.d.must(t, http.MethodGet, "/api/agents/default/turns/"+turn, nil, &opened)
	if opened.ID != childB || opened.AgentID != s.b.ID || opened.AgentName != "Coder" {
		t.Fatalf("turn of the child in B, opened in A: %+v", opened)
	}
	// But a turn of a chat in B that is no child of A is not found in A.
	var unrelated string
	if err := s.conn.QueryRowContext(t.Context(), `SELECT id FROM submissions WHERE conversation_id = ? AND kind = 'message'`, other.id).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	if status, _ := s.d.call(t, http.MethodGet, "/api/agents/default/turns/"+unrelated, nil); status != http.StatusNotFound {
		t.Fatalf("turn of an unrelated chat in B, opened in A: status %d", status)
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
	// When it searches all sessions with no bot, only the current bot answers.
	got := searcher.search(map[string]any{"query": "zebra", "session": "all"})
	want("empty bot", got, inA)
	if got[0].Bot != "Dex" || got[0].BotID != "default" {
		t.Fatalf("match does not name its bot: %+v", got[0])
	}
	// And bot all covers every bot, and each match names its bot. A dream stays hidden.
	got = searcher.search(map[string]any{"query": "zebra", "session": "all", "bot": "all"})
	want("all bots", got, inA, inB)
	for _, m := range got {
		if m.Session == fmt.Sprint(inB.id) && (m.Bot != "Coder" || m.BotID != s.b.ID) {
			t.Fatalf("match does not name bot B: %+v", m)
		}
	}
	// And a bot by name or by ID covers that bot only.
	want("bot by name", searcher.search(map[string]any{"query": "zebra", "session": "all", "bot": "coder"}), inB)
	want("bot by ID", searcher.search(map[string]any{"query": "zebra", "session": "all", "bot": s.b.ID}), inB)
	// And a session of another bot is found only when the search names that bot.
	want("session of another bot, default bot", searcher.search(map[string]any{"query": "zebra", "session": fmt.Sprint(inB.id)}))
	want("session of another bot, named bot", searcher.search(map[string]any{"query": "zebra", "session": fmt.Sprint(inB.id), "bot": "Coder"}), inB)
	// And an unknown bot, and a name that two bots share, fail with a message.
	if got := searcher.call("session_search", map[string]any{"query": "zebra", "session": "all", "bot": "Nobody"}); !strings.HasPrefix(got, "spawn error") || !strings.Contains(got, "no bot has") {
		t.Fatalf("unknown bot: %s", got)
	}
	var twin apiAgent
	s.d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Coder", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &twin, http.StatusCreated)
	if got := searcher.call("session_search", map[string]any{"query": "zebra", "session": "all", "bot": "Coder"}); !strings.Contains(got, "2 bots have the name") || !strings.Contains(got, twin.ID) || !strings.Contains(got, s.b.ID) {
		t.Fatalf("ambiguous bot: %s", got)
	}
	want("ambiguous name by ID", searcher.search(map[string]any{"query": "zebra", "session": "all", "bot": twin.ID}))
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

func TestUserChildLinksFilesInItsOwnBot(t *testing.T) {
	t.Parallel()
	s := startBots(t)
	writeFile(t, filepath.Join(s.dir, "agents", s.b.ID, "workspace", "report.markdown"), "# Report\n")
	parent := s.chat("default")
	child := parent.newChild(map[string]any{"bot": "Coder", "title": "Files", "message": `deliver_file {"path":"report.markdown"}`, "provider": "faux", "modelId": "tool"})
	waitResults(t, s.conn, parent.id, 1)
	if got := listArtifacts(t, s.d, "default"); len(got) != 0 {
		t.Fatalf("parent directory received child files: %+v", got)
	}
	files := listArtifacts(t, s.d, s.b.ID)
	if len(files) != 1 || files[0].Name != "report.markdown" {
		t.Fatalf("child bot files=%+v", files)
	}
	if _, body := readArtifact(t, s.d, s.b.ID, "report.markdown"); string(body) != "# Report\n" {
		t.Fatalf("child file=%q", body)
	}
	url := artifactPath(s.b.ID, "report.markdown")
	s.d.watchPath(t, fmt.Sprintf("/api/agents/%s/chats/%s/events", s.b.ID, child.ID)).until(t, "child returns actual bot URL", answeredWith(url))
	s.d.must(t, http.MethodGet, artifactPath("default", "report.markdown"), nil, nil, http.StatusNotFound)
}

func TestUserChildUsesTargetBotRuntimeContext(t *testing.T) {
	t.Parallel()
	s := startBots(t)
	writeSkill(t, filepath.Join(s.dir, "agents", "default", ".agents", "skills"), "a-only-skill", "description: A marker")
	writeSkill(t, filepath.Join(s.dir, "agents", s.b.ID, ".agents", "skills"), "b-only-skill", "description: B marker")
	writeFile(t, filepath.Join(s.dir, "memory", "agents", "default", "MEMORY.md"), "A_ONLY_MEMORY_MARKER\n")
	writeFile(t, filepath.Join(s.dir, "memory", "agents", s.b.ID, "MEMORY.md"), "B_ONLY_MEMORY_MARKER\n")
	aDir, bDir := t.TempDir(), t.TempDir()
	seedMCP(t, s.dir, "default", map[string]any{"afiles": stdioServer(t, aDir)})
	seedMCP(t, s.dir, s.b.ID, map[string]any{"bfiles": stdioServer(t, bDir)})
	if status, body := s.d.reload(t); status != http.StatusOK {
		t.Fatalf("reload: %d %s", status, body)
	}
	parent := s.chat("default")
	project := t.TempDir()
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "parent-project-only", "description: Parent project marker")
	s.d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/project", parent.id), map[string]string{"cwd": project}, nil)
	s.d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", parent.id), map[string]string{"provider": "faux", "modelId": "spawn", "thinkingLevel": "high"}, nil)
	run := func(model, message string) string {
		t.Helper()
		n := len(resultRows(t, s.conn, parent.id))
		child := parent.newChild(map[string]any{"bot": "Coder", "title": message, "message": message, "provider": "faux", "modelId": model})
		rows := waitResults(t, s.conn, parent.id, n+1)
		answer, _ := resultBody(t, rows[n].content)["answer"].(string)
		t.Logf("child %s model %s input %q answer %q", child.ID, model, message, answer)
		return answer
	}
	if got := run("section", "agent-memory"); !strings.Contains(got, "B_ONLY_MEMORY_MARKER") || strings.Contains(got, "A_ONLY_MEMORY_MARKER") {
		t.Errorf("memory: %s", got)
	}
	if got := run("section", "skills"); !strings.Contains(got, "b-only-skill") || strings.Contains(got, "a-only-skill") || strings.Contains(got, "parent-project-only") {
		t.Errorf("skills: %s", got)
	}
	if got := run("section", "instructions"); !strings.Contains(got, "Instruction marker of Coder") {
		t.Errorf("instructions: %s", got)
	}
	if got := run("bash", "pwd"); !strings.Contains(got, filepath.Join("agents", s.b.ID, "workspace")) {
		t.Errorf("workspace: %s", got)
	}
	if got := run("tool", crossMarkerCall); got != "tool: from-b" {
		t.Errorf("env: %s", got)
	}
	if got := run("think", "thinking marker"); !strings.Contains(got, "think off:") {
		t.Errorf("thinking default: %s", got)
	}
	if got := run("tool", `mcp__bfiles__append {"text":"b proof"}`); !strings.Contains(got, "appended 1") {
		t.Errorf("B MCP: %s", got)
	}
	if got := run("tool", `mcp__afiles__append {"text":"a proof"}`); !strings.Contains(got, "not available") {
		t.Errorf("A MCP leaked: %s", got)
	}
}
