package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"html"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/telegram"
)

func TestUserTaskCommandsDoNotCreateAChannelChat(t *testing.T) {
	// Given a peer that has never sent a model message.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	// When the peer queries tasks, including invalid command arguments.
	for i, command := range []string{"/tasks", "/task missing", "/cancel missing", "/task", "/cancel", "/tasks extra", "/help"} {
		f.message(int64(i+1), 42, 7, command)
		reply := html.UnescapeString(f.next(t).Text)
		if strings.Contains(reply, "echo:") || reply == "" {
			t.Fatalf("%s reached the model or has no answer: %q", command, reply)
		}
		if command == "/help" {
			for _, name := range []string{"/tasks", "/task <id>", "/cancel <id>"} {
				if !strings.Contains(reply, name) {
					t.Fatalf("help omits %s: %q", name, reply)
				}
			}
		}
	}
	// Then commands leave the model conversation list empty.
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("task commands created chats: %+v", chats)
	}
}

func TestUserTaskCommandsStayInCurrentChannelChat(t *testing.T) {
	// Given a task created through the real worker in this peer's first chat.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	task := spawnChannelTask(t, d, f, 1, "bounded result")
	conn := taskChannelDB(t, dir)
	waitTaskChannelDelivery(t, conn)
	drainTaskChannelMessages(f)
	// When the user asks for the task list and the full report.
	f.message(2, 42, 7, "/tasks")
	f.message(2, 42, 7, "/tasks")
	if got := f.next(t).Text; !strings.Contains(got, task.ShortID) || strings.Contains(got, "echo:") {
		t.Fatalf("task list: %q", got)
	}
	f.message(3, 42, 7, "/task "+task.ShortID)
	if got := f.next(t).Text; !strings.Contains(got, "echo: bounded result") {
		t.Fatalf("full task report: %q", got)
	}
	// Then a new chat and another peer cannot inspect or cancel the old task.
	f.message(4, 42, 7, "/new")
	if got := f.next(t).Text; got != "Started a new chat." {
		t.Fatal(got)
	}
	for i, command := range []string{"/tasks", "/task " + task.ShortID, "/cancel " + task.ShortID} {
		f.message(int64(i+5), 42, 7, command)
		want := "Task not found in this chat."
		if command == "/tasks" {
			want = "No tasks in this chat."
		}
		if got := f.next(t).Text; got != want {
			t.Fatalf("%s: %q", command, got)
		}
	}
	f.message(8, 42, 8, "/task "+task.ID)
	if got := f.next(t); got.ChatID != 8 || got.Text != "Task not found in this chat." {
		t.Fatalf("other peer: %+v", got)
	}
	var receipts int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM channel_commands WHERE command = '/tasks' AND channel_key = 'telegram:7'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("duplicate task-list receipts: %d", receipts)
	}
}

func TestUserTaskReportResumesAfterRestart(t *testing.T) {
	// Given a task with a report longer than one Telegram message.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	report := strings.Repeat("task report text ", 400)
	task := spawnChannelTask(t, d, f, 1, report)
	conn := taskChannelDB(t, dir)
	waitTaskChannelDelivery(t, conn)
	drainTaskChannelMessages(f)
	// When the first report part succeeds and the next part fails.
	f.failAfter.Store(f.sendCount.Load() + 1)
	f.message(2, 42, 7, "/task "+task.ShortID)
	first := f.next(t).Text
	var answer string
	eventually(t, "task report first part saved", func() (bool, any) {
		var parts int
		err := conn.QueryRowContext(t.Context(), "SELECT answer, delivered_parts FROM channel_commands WHERE command = ?", "/task "+task.ShortID).Scan(&answer, &parts)
		return err == nil && parts == 1, fmt.Sprintf("parts=%d err=%v", parts, err)
	})
	eventually(t, "task report next send failed", func() (bool, any) {
		n := f.sendCount.Load()
		return n > f.failAfter.Load(), n
	})
	d.stop()
	f.failAfter.Store(0)
	// Then restart sends only the remaining report parts from the durable receipt.
	startDex(t, dir, f.srv.URL)
	waitTaskChannelDelivery(t, conn)
	parts := []string{first}
	for len(f.sent) > 0 {
		parts = append(parts, (<-f.sent).Text)
	}
	want := new(telegram.Bot).Parts(chat.Reply{Status: "done", Answer: answer})
	if !slices.Equal(parts, want) {
		t.Fatalf("report parts mismatch: delivered=%q want=%q", parts, want)
	}
	if len(want) < 2 {
		t.Fatalf("report did not require multiple parts: %d", len(want))
	}
}

func TestUserStopsMainAnswerAndRequestsTaskCancellation(t *testing.T) {
	// Given a background stage that is still running after its parent's answer.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "spawn")
	f.message(1, 42, 7, `{"title":"Long task","goal":"sleep 60","provider":"faux","modelId":"bash"}`)
	var task userTask
	var chatID int64
	eventually(t, "running background task", func() (bool, any) {
		chats := d.chats(t)
		if len(chats) != 1 || chats[0].Busy {
			return false, chats
		}
		chatID = chats[0].ID
		tasks := d.tasks(t, chatID)
		if len(tasks) != 1 {
			return false, tasks
		}
		task = tasks[0]
		return len(task.Stages) == 1 && task.Stages[0].CurrentTool == "bash", task
	})
	// When /stop stops only the main answer, the background stage stays active.
	f.message(2, 42, 7, "/stop")
	waitTaskChannelText(t, f, "Stopped the current answer.")
	tasks := d.tasks(t, chatID)
	if len(tasks) != 1 || tasks[0].Status != "active" || tasks[0].Stages[0].Status != "running" {
		t.Fatalf("/stop changed background task: %+v", tasks)
	}
	// Then /cancel confirms only that the cancellation request was accepted.
	f.message(3, 42, 7, "/cancel "+task.ShortID)
	f.message(3, 42, 7, "/cancel "+task.ShortID)
	waitTaskChannelText(t, f, "Cancellation accepted for "+task.ShortID)
}

func TestUserGetsTaskNoticeAfterRestart(t *testing.T) {
	// Given task results whose outbound delivery fails at the Bot API boundary.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	f.message(1, 42, 7, "/new")
	if got := f.next(t).Text; got != "Started a new chat." {
		t.Fatal(got)
	}
	f.failSend.Store(true)
	task := spawnChannelTask(t, d, f, 2, "notice result")
	conn := taskChannelDB(t, dir)
	eventually(t, "durable task notice", func() (bool, any) {
		var count int
		err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM task_notices WHERE user_task_id = ? AND delivered_at IS NULL AND channel_key LIKE 'telegram:7/%'", task.ID).Scan(&count)
		return err == nil && count > 0, fmt.Sprintf("count=%d err=%v", count, err)
	})
	// When control restarts with the same storage and a working transport.
	d.stop()
	f.failSend.Store(false)
	startDex(t, dir, f.srv.URL)
	waitTaskChannelDelivery(t, conn)
	// Then persisted notices reach the original peer, without another user message.
	var found bool
	for {
		select {
		case reply := <-f.sent:
			if reply.ChatID != 7 {
				t.Fatalf("notice sent to another peer: %+v", reply)
			}
			found = found || strings.Contains(reply.Text, task.ShortID)
		default:
			if !found {
				t.Fatal("no task notice delivered after restart")
			}
			return
		}
	}
}

func TestUserReceivesEachTaskQuestionInChannel(t *testing.T) {
	// Given a channel goal with a completed first stage.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	task := spawnChannelTask(t, d, f, 1, "first report")
	conn := taskChannelDB(t, dir)
	waitTaskChannelDelivery(t, conn)
	drainTaskChannelMessages(f)
	finish := func(updateID int64, question string) {
		t.Helper()
		d.setModel(t, "tool")
		f.message(updateID, 42, 7, "finish_task "+taskJSON(t, map[string]string{"id": task.ID, "status": "needs_input", "report": question}))
		eventually(t, "current task question", func() (bool, any) {
			got := getUserTask(t, d, task.ChatID, task.ID)
			return got.Status == "needs_input" && got.Report == question, got
		})
		waitTaskChannelDelivery(t, conn)
	}
	// When the goal asks a question, continues, and asks another question.
	finish(2, "Question one")
	d.setModel(t, "spawn")
	f.message(3, 42, 7, taskJSON(t, map[string]string{"title": "Next stage", "goal": "second report", "userTaskId": task.ID, "provider": "faux", "modelId": "echo"}))
	waitUserTask(t, d, task.ChatID, task.Title, "done", 2)
	waitTaskChannelDelivery(t, conn)
	finish(4, "Question two")
	// Then both questions have their own direct status notice.
	var first, second bool
	for len(f.sent) > 0 {
		text := html.UnescapeString((<-f.sent).Text)
		if strings.HasPrefix(text, "Task "+task.ShortID) && strings.Contains(text, "Status: needs_input") {
			first = first || strings.Contains(text, "Question one")
			second = second || strings.Contains(text, "Question two")
		}
	}
	if !first || !second {
		t.Fatalf("question notices: first=%t second=%t", first, second)
	}
}

func spawnChannelTask(t *testing.T, d *dex, f *fakeTelegram, updateID int64, goal string) userTask {
	t.Helper()
	d.setModel(t, "spawn")
	args, err := json.Marshal(map[string]string{"title": "Channel task", "goal": goal, "provider": "faux", "modelId": "echo"})
	if err != nil {
		t.Fatal(err)
	}
	f.message(updateID, 42, 7, string(args))
	var task userTask
	eventually(t, "channel task report", func() (bool, any) {
		chats := d.chats(t)
		if len(chats) != 1 {
			return false, chats
		}
		tasks := d.tasks(t, chats[0].ID)
		if len(tasks) != 1 {
			return false, tasks
		}
		task = tasks[0]
		return len(task.Stages) == 1 && task.Stages[0].Status == "done", tasks
	})
	return task
}

func taskChannelDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitTaskChannelDelivery(t *testing.T, conn *sql.DB) {
	t.Helper()
	eventually(t, "all channel messages delivered", func() (bool, any) {
		var count int
		err := conn.QueryRowContext(t.Context(), `SELECT
			(SELECT count(*) FROM submissions s JOIN conversations c ON c.id = s.conversation_id
			 WHERE s.status = 'pending' OR (c.channel_key LIKE 'telegram:%' AND s.delivered_at IS NULL)) +
			(SELECT count(*) FROM channel_commands WHERE delivered_at IS NULL) +
			(SELECT count(*) FROM task_notices WHERE delivered_at IS NULL)`).Scan(&count)
		return err == nil && count == 0, fmt.Sprintf("pending=%d err=%v", count, err)
	})
}

func drainTaskChannelMessages(f *fakeTelegram) {
	for {
		select {
		case <-f.sent:
		default:
			return
		}
	}
}

func waitTaskChannelText(t *testing.T, f *fakeTelegram, text string) {
	t.Helper()
	var replies []string
	eventually(t, text, func() (bool, any) {
		for {
			select {
			case reply := <-f.sent:
				replies = append(replies, reply.Text)
				if strings.Contains(reply.Text, text) {
					return true, replies
				}
			default:
				return false, replies
			}
		}
	})
}
