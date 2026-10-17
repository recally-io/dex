package app_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type channelSend struct {
	Peer string
	Text string
}

func assertNoStoredInput(t *testing.T, d *dex) {
	t.Helper()
	var count int
	if err := sessionDB(t, d).QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE kind='message'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stored ordinary input=%d want=0", count)
	}
}

// The adapters use the existing platform servers. Control and the worker stay real.
type mainEntry struct {
	d     *dex
	dir   string
	peer  string
	send  func(string)
	sends func() []channelSend
}

func newMainEntry(t *testing.T, platform string) mainEntry {
	t.Helper()
	e := mainEntry{dir: t.TempDir()}
	var serial int64
	switch platform {
	case "telegram":
		f := newFakeTelegram(t)
		e.d = startTelegram(t, f, e.dir, 42)
		e.peer = "telegram:7"
		e.send = func(text string) { serial++; f.message(serial, 42, 7, text) }
		e.sends = func() []channelSend {
			f.mu.Lock()
			defer f.mu.Unlock()
			return slices.Clone(f.deliveries)
		}
	case "wechat":
		f := newFakeWechat(t)
		e.d = startWechat(t, f, e.dir)
		var login wechatLogin
		e.d.must(t, http.MethodPost, "/api/agents/default/channels/wechat/login", nil, &login)
		e.d.must(t, http.MethodPost, "/api/agents/default/channels/wechat/login/"+login.ID+"/poll", map[string]string{}, &login)
		e.peer = "wechat:owner"
		e.send = func(text string) { serial++; f.batch(fmt.Sprint(serial), wxText(uint64(serial), "owner", text)) }
		e.sends = func() []channelSend {
			f.mu.Lock()
			defer f.mu.Unlock()
			var out []channelSend
			for _, r := range f.requests {
				if r.Path != "/ilink/bot/sendmessage" {
					continue
				}
				raw, err := json.Marshal(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var sent wechatSent
				if err := json.Unmarshal(raw, &sent); err != nil {
					t.Fatal(err)
				}
				out = append(out, channelSend{Peer: "wechat:" + sent.Msg.To, Text: wxSentText(sent)})
			}
			return out
		}
	case "feishu":
		f := newFakeFeishu(t)
		e.d = startFeishu(t, f, e.dir)
		pairFeishu(t, e.d, f)
		e.peer = "feishu:owner"
		e.send = func(text string) { serial++; f.text(t, fmt.Sprint(serial), "owner", "p2p", text) }
		e.sends = func() []channelSend {
			f.mu.Lock()
			defer f.mu.Unlock()
			return slices.Clone(f.deliveries)
		}
	default:
		t.Fatal(platform)
	}
	return e
}

func (e mainEntry) wait(t *testing.T, text string) {
	t.Helper()
	eventually(t, "channel send "+text, func() (bool, any) {
		sends := e.sends()
		return slices.Contains(sends, channelSend{Peer: e.peer, Text: text}), sends
	})
}

func TestUserChannelMainSharesContextAndRoutesReplies(t *testing.T) {
	for _, platform := range []string{"telegram", "wechat", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			// Given a separate Chat and an old peer conversation with a model override.
			e := newMainEntry(t, platform)
			d := e.d
			conn := sessionDB(t, d)
			main := d.mainChat(t)
			old := sessionFixture(t, d, e.peer+"/old", 1)
			if _, err := conn.ExecContext(t.Context(), "UPDATE conversations SET model_provider='faux', model_id='shout' WHERE id=?", old); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), "INSERT INTO channel_peers (agent_id, peer, channel_key) VALUES ('default', ?, ?)", e.peer, e.peer+"/old"); err != nil {
				t.Fatal(err)
			}
			var chat chatInfo
			d.must(t, http.MethodPost, "/api/agents/default/chats", nil, &chat, http.StatusCreated)
			d.setModel(t, "context")
			d.send(t, chat.ID, "Chat secret")
			d.watch(t, chat.ID).until(t, "separate Chat", answered(`context: ["Chat secret"]`))
			web := d.send(t, main, "web context")
			waitResultSubmission(t, conn, web, "done")
			w := d.watch(t, main)
			w.until(t, "web context before channel input", answered(`context: ["web context"]`))

			// When the owner sends from the channel, the model reads main's context.
			e.send("channel context")
			e.wait(t, `context: ["web context","channel context"]`)
			var id string
			var conversation int64
			if err := conn.QueryRowContext(t.Context(), "SELECT id, conversation_id FROM submissions WHERE origin=? AND kind='message'", e.peer).Scan(&id, &conversation); err != nil {
				t.Fatal(err)
			}
			if conversation != main {
				t.Fatalf("channel input entered %d; main=%d", conversation, main)
			}
			w.until(t, "channel entry mark in live stream", func(tr *transcript) bool {
				return slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == id && it.Origin == e.peer })
			})
			// Then web input produces no channel reply, and another channel turn proves delivery passed it.
			d.setModel(t, "echo")
			web = d.send(t, main, "web answer only")
			waitResultSubmission(t, conn, web, "done")
			e.send("delivery barrier")
			e.wait(t, "echo: delivery barrier")
			for _, sent := range e.sends() {
				if sent.Text == "echo: web answer only" {
					t.Fatalf("web reply reached channel: %+v", sent)
				}
			}
			for _, c := range d.chats(t) {
				if c.Channel == platform || c.ID == old {
					t.Fatalf("old channel in Sessions: %+v", c)
				}
			}
			var n int
			if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id=?", old).Scan(&n); err != nil || n != 0 {
				t.Fatalf("old context got input: %d %v", n, err)
			}
			// A channel clear does not reach the separate Chat. Earlier input keeps its entry mark.
			e.send("/new")
			e.wait(t, "Context cleared.")
			d.setModel(t, "context")
			e.send("after clear")
			e.wait(t, `context: ["after clear"]`)
			for _, sent := range e.sends() {
				if sent.Text == "(empty answer)" {
					t.Fatalf("Clear sent a model reply: %+v", sent)
				}
			}
			d.send(t, chat.ID, "still separate")
			d.watch(t, chat.ID).until(t, "Chat survives main clear", answered(`context: ["Chat secret","still separate"]`))
			var page struct {
				Items []item `json:"items"`
			}
			d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/history?before=9007199254740991", main), nil, &page)
			if !slices.ContainsFunc(page.Items, func(it item) bool { return it.ID == id && it.Origin == e.peer }) {
				t.Fatalf("history lost channel entry: %+v", page.Items)
			}
			t.Logf("artifact: main=%d old=%d Chat=%d origin=%s sends=%+v", main, old, chat.ID, e.peer, e.sends())
		})
	}
}

func TestUserChannelMainTaskResultsFollowTheirSource(t *testing.T) {
	for _, platform := range []string{"telegram", "wechat", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			// Given channel work that will finish after a newer web message.
			e := newMainEntry(t, platform)
			d := e.d
			conn := sessionDB(t, d)
			main := d.mainChat(t)
			d.setModel(t, "spawn")
			e.send(taskJSON(t, map[string]string{"title": "Channel result", "goal": taskGate(t, e.dir, "channel-result"), "provider": "faux", "modelId": "bash"}))
			waitTaskFile(t, filepath.Join(e.dir, "channel-result.pid"))
			var parent int64
			if err := conn.QueryRowContext(t.Context(), "SELECT parent_conversation_id FROM tasks WHERE title='Channel result'").Scan(&parent); err != nil {
				t.Fatal(err)
			}
			if parent != main {
				t.Fatalf("channel work parent=%d main=%d", parent, main)
			}
			work := waitExecution(t, conn, main, "Channel result", "running")
			web := d.send(t, main, taskJSON(t, map[string]string{"title": "Web result", "goal": "web report", "provider": "faux", "modelId": "echo"}))
			waitResultSubmission(t, conn, web, "done")
			webTask := waitExecution(t, conn, main, "Web result", "done")
			webResult := waitResultSubmission(t, conn, "task:"+webTask.ID+":result", "done")
			// When the old channel work completes, its result returns to its source peer.
			releaseResultGate(t, e.dir, "channel-result")
			resultID := "task:" + work.ID + ":result"
			result := waitResultSubmission(t, conn, resultID, "done")
			e.wait(t, result.answer)
			assertSubmissionOrigin(t, conn, resultID, e.peer)
			for _, sent := range e.sends() {
				if sent.Text == webResult.answer {
					t.Fatalf("web task result reached channel: %+v", sent)
				}
			}
			t.Logf("artifact: channel=%s result=%s answer=%q web_result=%s sends=%+v", e.peer, resultID, result.answer, webTask.ID, e.sends())
		})
	}
}

func TestUserChannelMainBusyInputsKeepAdmissionOrder(t *testing.T) {
	// Given main held in a shell tool, with two channel destinations and one web input.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	main := d.mainChat(t)
	conn := sessionDB(t, d)
	d.setModel(t, "bash")
	d.send(t, main, taskGate(t, dir, "main-busy"))
	waitTaskFile(t, filepath.Join(dir, "main-busy.pid"))
	// When inputs arrive in a known order while main is busy.
	for i, peer := range []int64{7, 8} {
		f.message(int64(i+1), 42, peer, "printf peer-"+strconv.FormatInt(peer, 10))
		var admitted int
		eventually(t, "channel admission", func() (bool, any) {
			err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE origin=?", fmt.Sprint("telegram:", peer)).Scan(&admitted)
			return err == nil && admitted == 1, admitted
		})
	}
	web := d.send(t, main, "printf web-only")
	d.watch(t, main).until(t, "queued channel entry marks", func(tr *transcript) bool {
		for _, origin := range []string{"telegram:7", "telegram:8"} {
			if !slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "user" && it.Origin == origin && it.Queued }) {
				return false
			}
		}
		return true
	})
	// Wall time is deliberately opposite to admission order.
	if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET created_at=CASE origin WHEN 'telegram:7' THEN 300 WHEN 'telegram:8' THEN 200 ELSE 100 END WHERE status='pending'"); err != nil {
		t.Fatal(err)
	}
	releaseResultGate(t, dir, "main-busy")
	// Then each peer receives its own answer, in admission order, and web receives its own answer.
	for _, peer := range []int64{7, 8} {
		got := f.next(t)
		if got.ChatID != peer || got.Text != fmt.Sprint("bash: peer-", peer) {
			t.Fatalf("crossed reply or order: %+v want peer=%d", got, peer)
		}
	}
	if got := waitResultSubmission(t, conn, web, "done"); got.answer != "bash: web-only" {
		t.Fatal(got)
	}
	var rows []string
	query, err := conn.QueryContext(t.Context(), "SELECT origin, answer FROM submissions WHERE conversation_id=? ORDER BY rowid", main)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()
	for query.Next() {
		var origin, answer string
		if err := query.Scan(&origin, &answer); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, origin+"="+answer)
	}
	if err := query.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("inputs missed main: %q", rows)
	}
	t.Logf("artifact: admission order=%q", rows)
}

func TestUserChannelMainFeishuPreviewStopsBeforeQueuedWebInput(t *testing.T) {
	// Given a channel answer that streams while a web input waits in main.
	f := newFakeFeishu(t)
	dir := t.TempDir()
	d := startFeishu(t, f, dir)
	pairFeishu(t, d, f)
	d.setModel(t, "bash")
	main := d.mainChat(t)
	gate := taskGate(t, dir, "feishu-preview")
	text := strings.TrimSpace(strings.Repeat("channel-answer ", 400))
	f.text(t, "channel-preview", "owner", "p2p", gate+"; printf '%s' '"+text+"'")
	waitTaskFile(t, filepath.Join(dir, "feishu-preview.pid"))
	f.waitReply(t, "Thinking")
	web := d.send(t, main, "printf web-private")
	d.watch(t, main).until(t, "queued web input", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == web && it.Queued })
	})
	// When the channel answer streams, queued web input must not hide its preview.
	releaseResultGate(t, dir, "feishu-preview")
	eventually(t, "partial channel preview at the send boundary", func() (bool, any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, sent := range f.deliveries {
			if sent.Peer == "feishu:owner" && strings.Contains(sent.Text, "channel-answer") && len(sent.Text) < 1000 {
				return true, f.deliveries
			}
		}
		return false, f.deliveries
	})
	waitResultSubmission(t, taskResultDB(t, dir), web, "done")
	// Then neither the preview nor the final channel answer contains the web answer.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sent := range f.deliveries {
		if strings.Contains(sent.Text, "web-private") {
			t.Fatalf("web answer in channel preview: %+v", sent)
		}
	}
	t.Logf("artifact: channel preview parts=%d; queued web answer stayed in main", len(f.deliveries))
}

func TestUserChannelMainClearRefusesBusyAndDeduplicates(t *testing.T) {
	// Given main running a web input. Channel Clear must refuse without changing context.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	d.setModel(t, "bash")
	d.send(t, main, taskGate(t, dir, "channel-clear"))
	waitTaskFile(t, filepath.Join(dir, "channel-clear.pid"))
	f.message(1, 42, 7, "/new")
	waitTaskChannelText(t, f, "Context not cleared. The main timeline is busy.")
	releaseResultGate(t, dir, "channel-clear")
	d.watch(t, main).until(t, "web answer after refusal", answered("bash: gate released"))
	// When the platform replays the refused command after main becomes idle, it stays refused.
	f.message(1, 42, 7, "/new")
	f.message(2, 42, 7, "/new")
	waitTaskChannelText(t, f, "Context cleared.")
	f.message(2, 42, 7, "/new")
	f.message(3, 42, 7, "printf after-clear")
	waitTaskChannelText(t, f, "bash: after-clear")
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE kind='context_reset' AND conversation_id=?", main).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("channel Clear made %d resets", count)
	}
	// A control restart between reset commit and command completion must reuse the reset.
	d.stop()
	if _, err := conn.ExecContext(t.Context(), "UPDATE channel_commands SET completed=0, delivered_at=NULL WHERE source_key='telegram:123:2'"); err != nil {
		t.Fatal(err)
	}
	startDex(t, dir, f.srv.URL)
	waitTaskChannelText(t, f, "Context cleared.")
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE kind='context_reset' AND conversation_id=?", main).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("replayed Clear made %d resets", count)
	}
	t.Logf("artifact: channel clear resets=%d; refused and successful receipts survive replay", count)
}

func TestUserChannelMainStopKeepsChatAndAutomationWork(t *testing.T) {
	// Given work started from two main entries, a separate Chat, and an automation.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	d.setModel(t, "spawn")
	f.message(1, 42, 7, taskJSON(t, map[string]string{"title": "main task", "goal": taskGate(t, dir, "main-task"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "main-task.pid"))
	var parent int64
	if err := conn.QueryRowContext(t.Context(), "SELECT parent_conversation_id FROM tasks WHERE title='main task'").Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != main {
		t.Fatalf("channel task outside main: %d != %d", parent, main)
	}
	d.setModel(t, "bash")
	chat := d.newChat(t, taskGate(t, dir, "separate-chat"))
	waitTaskFile(t, filepath.Join(dir, "separate-chat.pid"))
	auto := d.createAutomation(t, "Separate automation", taskGate(t, dir, "separate-auto"), "new", map[string]any{"kind": "every", "minutes": 60})
	d.runNow(t, auto.ID)
	waitTaskFile(t, filepath.Join(dir, "separate-auto.pid"))
	web := d.send(t, main, taskGate(t, dir, "main-turn"))
	waitTaskFile(t, filepath.Join(dir, "main-turn.pid"))
	f.message(2, 42, 8, "printf queued-peer")
	// When another channel entry stops main, main input and tasks are cancelled together.
	f.message(3, 42, 8, "/stop")
	waitTaskChannelText(t, f, "Stop requested.")
	waitResultSubmission(t, conn, web, "unanswered")
	waitExecution(t, conn, main, "main task", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "main-task.pid"))
	waitTaskPIDGone(t, filepath.Join(dir, "main-turn.pid"))
	for _, name := range []string{"separate-chat", "separate-auto"} {
		if taskPIDGone(t, filepath.Join(dir, name+".pid")) {
			t.Fatalf("main Stop crossed into %s", name)
		}
		releaseResultGate(t, dir, name)
	}
	d.watch(t, chat).until(t, "Chat work preserved", answered("bash: gate released"))
	var pending int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id=? AND status='pending'", main).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("main has pending input: %d %v", pending, err)
	}
	t.Logf("artifact: stopped main=%d; preserved Chat=%d and automation=%s", main, chat, auto.ID)
}
