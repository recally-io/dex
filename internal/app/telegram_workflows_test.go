package app_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func (f *fakeTelegram) callback(updateID, sender, chat, topic, remote int64, data string) {
	f.lastCallback.Store(updateID)
	f.mu.Lock()
	defer f.mu.Unlock()
	kind := "private"
	if chat < 0 {
		kind = "supergroup"
	}
	f.updates = append(f.updates, map[string]any{"update_id": updateID, "callback_query": map[string]any{"id": fmt.Sprint(updateID), "from": map[string]any{"id": sender}, "data": data, "message": map[string]any{"message_id": remote, "from": map[string]any{"id": 123, "is_bot": true}, "chat": map[string]any{"id": chat, "type": kind, "is_forum": topic > 0}, "message_thread_id": topic}}})
}

func (f *fakeTelegram) button(t *testing.T, message int64, text string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.keyboards[message] {
		for _, button := range row {
			if strings.Contains(button.Text, text) {
				if len(button.Data) > 64 {
					t.Fatalf("callback exceeds Telegram's byte limit: %d", len(button.Data))
				}
				return button.Data
			}
		}
	}
	t.Fatalf("no %q button on message %d: %+v", text, message, f.keyboards[message])
	return ""
}

func (f *fakeTelegram) callbackResult(t *testing.T) platformEvent {
	t.Helper()
	id := f.lastCallback.Load()
	for range 30 {
		event := f.nextEvent(t, "answerCallbackQuery")
		if event.MessageID == id {
			return event
		}
	}
	t.Fatalf("callback %d had no matching acknowledgement", id)
	return platformEvent{}
}

func TestUserTelegramGroupMenuDoesNotEnterPrivateMain(t *testing.T) {
	t.Parallel()
	// Given a paired sender in a group topic and an empty private Main.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	// When the sender opens the menu without a mention.
	f.groupText(1, 42, -100, 7, "/menu")
	got := f.next(t)
	// Then the current audience gets controls, not a model echo or private owner controls.
	if got.ChatID != -100 || got.TopicID != 7 || !strings.Contains(got.Text, "Current conversation") || strings.Contains(got.Text, "echo: /menu") {
		t.Fatalf("group menu was not handled in its audience: %+v", got)
	}
	f.button(t, got.ID, "Stop")
}

func TestUserTelegramGroupQuestionBindsSignedAnswerToActorAudienceAndOneUse(t *testing.T) {
	t.Parallel()
	// Given a public question asked through the actual isolated native tool.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42, 43)
	d.setModel(t, "spawn")
	f.groupText(1, 42, -100, 7, `{"tool":"group_question","question":"Choose the public result","options":["Red","Blue"]}`)
	question := f.next(t)
	if !strings.Contains(question.Text, "Choose the public result") || question.TopicID != 7 {
		t.Fatalf("public question=%+v", question)
	}
	button := f.button(t, question.ID, "Red")
	conn := controlWriteDB(t, data)
	// When a signed button is moved, forged or pressed by another member, it cannot answer.
	attempts := []struct {
		actor, chat, topic, remote int64
		data                       string
	}{{43, -100, 7, question.ID, button}, {99, -100, 7, question.ID, button}, {42, -200, 7, question.ID, button}, {42, -100, 8, question.ID, button}, {42, -100, 7, question.ID + 100, button}, {42, -100, 7, question.ID, button + "x"}}
	for i, attempt := range attempts {
		f.callback(int64(i+2), attempt.actor, attempt.chat, attempt.topic, attempt.remote, attempt.data)
		event := f.callbackResult(t)
		if event.Text == "Accepted." {
			t.Fatalf("invalid binding accepted: %+v", attempt)
		}
	}
	var status string
	if err := conn.QueryRowContext(t.Context(), "SELECT status FROM questions LIMIT 1").Scan(&status); err != nil || status != "open" {
		t.Fatalf("invalid actor consumed question: %s %v", status, err)
	}
	// Then the original actor can answer once, and the reply stays in the same topic.
	d.setModel(t, "echo")
	f.callback(20, 42, -100, 7, question.ID, button)
	if got := f.callbackResult(t); got.Text != "Accepted." {
		t.Fatalf("valid choice=%+v", got)
	}
	for {
		got := f.next(t)
		if strings.Contains(got.Text, "Answer to:") {
			if got.ChatID != -100 || got.TopicID != 7 || !strings.Contains(got.Text, "Red") {
				t.Fatalf("answer left its audience: %+v", got)
			}
			break
		}
	}
	f.callback(21, 42, -100, 7, question.ID, button)
	if got := f.callbackResult(t); got.Text == "Accepted." {
		t.Fatalf("callback replay accepted: %+v", got)
	}
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key LIKE 'telegram-callback:%'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("answer replay count=%d err=%v", count, err)
	}
}

func TestUserTelegramCallbackExpiryRevocationAndClearGenerationSurviveRestart(t *testing.T) {
	t.Parallel()
	// Given durable menu buttons on an existing group conversation.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	f.groupText(1, 42, -100, 7, "public first input")
	f.next(t)
	f.groupText(2, 42, -100, 7, "/menu")
	menu := f.next(t)
	status := f.button(t, menu.ID, "Status")
	stop := f.button(t, menu.ID, "Stop")
	clearButton := f.button(t, menu.ID, "Clear")
	conn := controlWriteDB(t, data)
	// Expiry rejects the immutable action even while the sender is paired.
	if _, err := conn.ExecContext(t.Context(), "UPDATE telegram_callbacks SET expires_at=? WHERE action='/status'", time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	f.callback(3, 42, -100, 7, menu.ID, status)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("expired callback executed")
	}
	// A current revocation permanently cancels that button; regrant does not revive it.
	execSQL(t, conn, "UPDATE telegram_settings SET allowed_users='43' WHERE agent_id='default'")
	f.callback(4, 42, -100, 7, menu.ID, stop)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("revoked callback executed")
	}
	execSQL(t, conn, "UPDATE telegram_settings SET allowed_users='42' WHERE agent_id='default'")
	f.callback(5, 42, -100, 7, menu.ID, stop)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("regrant revived canceled button")
	}
	// After restart, an unconsumed clear still targets its original group generation.
	d.stop()
	startTelegram(t, f, data, 42)
	f.callback(6, 42, -100, 7, menu.ID, clearButton)
	if event := f.callbackResult(t); event.Text != "Accepted." {
		t.Fatalf("durable callback lost on restart: %+v", event)
	}
	if got := f.next(t); got.ChatID != -100 || got.TopicID != 7 || got.Text != "Context cleared." {
		t.Fatalf("clear target=%+v", got)
	}
	// Another unconsumed button from the old generation cannot act on the new native conversation.
	stale := f.button(t, menu.ID, "Settings")
	f.callback(7, 42, -100, 7, menu.ID, stale)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("old generation callback acted after clear")
	}
}

func TestUserTelegramCallbackAcknowledgementFailureKeepsPollingSafe(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		failure   platformFailure
		permanent bool
	}{
		{"rejected", platformFailure{Code: 400}, true},
		{"service failure", platformFailure{Code: 500}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			f := newFakeTelegram(t)
			startTelegram(t, f, data, 42)
			f.message(1, 42, 7, "/menu")
			menu := f.next(t)
			status := f.button(t, menu.ID, "Status")
			f.mu.Lock()
			f.failures["answerCallbackQuery"] = []platformFailure{scenario.failure, scenario.failure}
			f.mu.Unlock()

			f.callback(2, 42, 7, 0, menu.ID, status)
			f.message(3, 42, 7, "after the button")

			answers, entered := 0, false
			for answers == 0 || !entered {
				got := f.next(t)
				if strings.Contains(got.Text, "Current conversation") {
					answers++
				}
				if got.Text == "echo: after the button" {
					entered = true
					f.mu.Lock()
					left := len(f.failures["answerCallbackQuery"])
					f.mu.Unlock()
					if want := map[bool]int{true: 1, false: 0}[scenario.permanent]; left != want {
						t.Fatalf("unused acknowledgement failures=%d, want %d", left, want)
					}
				}
			}
			var intents, consumed int
			conn := controlWriteDB(t, data)
			if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM channel_commands WHERE source_key LIKE 'telegram-callback:%'").Scan(&intents); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM telegram_callbacks WHERE consumed_at IS NOT NULL").Scan(&consumed); err != nil {
				t.Fatal(err)
			}
			if answers != 1 || intents != 1 || consumed != 1 {
				t.Fatalf("status answers=%d intents=%d consumed buttons=%d, want one each", answers, intents, consumed)
			}
		})
	}
}

func TestUserTelegramSavedWorkflowStaysInAudienceAndUsesItsInitiatingActor(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42, 43)
	d.setModel(t, "spawn")
	f.groupText(1, 42, -100, 7, `{"tool":"group_session","action":"start","title":"Public saved workflow","message":"{\"tool\":\"group_note\",\"text\":\"public workflow note\"}"}`)
	_ = f.next(t) // initial group_session dispatch reply
	conn := controlWriteDB(t, data)
	eventually(t, "group workflow task_result settles done", func() (bool, any) {
		var n int
		err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE kind='task_result' AND status='done'").Scan(&n)
		return err == nil && n == 1, n
	})
	var child int64
	var scope string
	if err := conn.QueryRowContext(t.Context(), "SELECT id,execution_scope FROM conversations WHERE parent_id IS NOT NULL").Scan(&child, &scope); err != nil {
		t.Fatal(err)
	}
	f.groupText(2, 42, -100, 7, "/workflows")
	menu := f.next(t)
	if !strings.Contains(menu.Text, "Public saved workflow") {
		t.Fatalf("saved workflow missing: %+v", menu)
	}
	resume := f.button(t, menu.ID, "Resume")
	f.groupText(3, 42, -100, 8, "/workflows")
	if got := f.next(t); strings.Contains(got.Text, "Public saved workflow") {
		t.Fatalf("workflow list crossed topic: %+v", got)
	}
	f.groupText(4, 42, -100, 8, fmt.Sprintf("/run %d", child))
	if got := f.next(t); !strings.Contains(got.Text, "not available") {
		t.Fatalf("workflow run crossed topic: %+v", got)
	}
	f.groupText(5, 43, -100, 7, fmt.Sprintf("/run %d", child))
	if got := f.next(t); !strings.Contains(got.Text, "not available") {
		t.Fatalf("workflow borrowed another actor: %+v", got)
	}
	d.setModel(t, "echo")
	f.callback(6, 42, -100, 7, menu.ID, resume)
	if event := f.callbackResult(t); event.Text != "Accepted." {
		t.Fatalf("saved workflow callback=%+v", event)
	}
	eventually(t, "resumed workflow task_result settles done in topic", func() (bool, any) {
		var content, origin string
		err := conn.QueryRowContext(t.Context(), "SELECT content, origin FROM submissions WHERE kind='task_result' AND status='done' AND content LIKE '%note-%'").Scan(&content, &origin)
		if err != nil {
			return false, err
		}
		if origin != "telegram:-100:7" {
			t.Fatalf("resumed workflow went to wrong origin %q: %s", origin, content)
		}
		return true, origin
	})
	var turns, private int
	var resumedScope string
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id=? AND parent_request_id IS NOT NULL", child).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT execution_scope FROM conversations WHERE id=?", child).Scan(&resumedScope); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM conversations WHERE channel_key='main'").Scan(&private); err != nil {
		t.Fatal(err)
	}
	if turns != 2 || resumedScope != scope || private != 0 {
		t.Fatalf("saved workflow moved or repeated: turns=%d scope=%s privateMain=%d", turns, resumedScope, private)
	}
}
