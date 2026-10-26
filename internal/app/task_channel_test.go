package app_test

import (
	"html"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserChannelHelpHasNoTaskManagementCommands(t *testing.T) {
	t.Parallel()
	// Given a channel peer without a model conversation.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	// When the peer opens the command menu and requests help.
	menu := f.nextEvent(t, "setMyCommands").Text
	f.message(1, 42, 7, "/help")
	reply := html.UnescapeString(f.next(t).Text)
	// Then neither entry point exposes task management or creates a model conversation.
	if menu != "/new /stop /help " {
		t.Fatalf("command menu: %q", menu)
	}
	for _, command := range []string{"/tasks", "/task ", "/cancel"} {
		if strings.Contains(reply, command) {
			t.Fatalf("obsolete command %s in %q", command, reply)
		}
	}
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("help created chats: %+v", chats)
	}
}

func TestUserChannelStopCoversMainAcrossClearAndPreservesChat(t *testing.T) {
	t.Parallel()
	// Given two channel entries with running work on main and a separate Chat.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	spawn := func(update, peer int64, name string) int64 {
		t.Helper()
		f.message(update, 42, peer, taskJSON(t, map[string]string{"title": name, "goal": taskGate(t, dir, name), "provider": "faux", "modelId": "bash"}))
		var parent int64
		eventually(t, "parent "+name, func() (bool, any) {
			err := conn.QueryRowContext(t.Context(), "SELECT parent_conversation_id FROM tasks WHERE title = ?", name).Scan(&parent)
			return err == nil, err
		})
		waitExecution(t, conn, parent, name, "running")
		waitTaskFile(t, filepath.Join(dir, name+".pid"))
		waitTaskChannelText(t, f, "spawn: ")
		return parent
	}
	old := spawn(1, 7, "old-peer")
	other := spawn(2, 8, "other-peer")
	d.setModel(t, "bash")
	web := d.newChat(t, taskGate(t, dir, "web-main"))
	waitTaskFile(t, filepath.Join(dir, "web-main.pid"))
	// When /new changes context and /stop is sent before another model input.
	f.message(3, 42, 7, "/new")
	waitTaskChannelText(t, f, "Context cleared.")
	if got := waitExecution(t, conn, old, "old-peer", "running"); got.Status != "running" {
		t.Fatal(got)
	}
	f.message(4, 42, 7, "/stop")
	waitExecution(t, conn, old, "old-peer", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "old-peer.pid"))
	// Then Stop covers both main entries across Clear and preserves the separate Chat.
	waitExecution(t, conn, other, "other-peer", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "other-peer.pid"))
	if taskPIDGone(t, filepath.Join(dir, "web-main.pid")) {
		t.Fatal("channel Stop crossed into Web chat")
	}
	releaseResultGate(t, dir, "web-main")
	d.watch(t, web).until(t, "unaffected web answer", answeredWith("gate released"))
	d.setModel(t, "echo")
	f.message(5, 42, 7, "hello")
	waitTaskChannelText(t, f, "echo: hello")
	var resultCount int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id = ? AND kind = 'task_result'", old).Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if resultCount != 0 {
		t.Fatalf("stopped execution delivered %d results", resultCount)
	}
	t.Logf("stopped_parent=%d second_entry_parent=%d results=%d", old, other, resultCount)
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
