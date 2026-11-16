package app_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestUserTelegramSavedWorkflowStaysInAudienceAndUsesItsInitiatingActor(t *testing.T) {
	// Given a saved direct child created and executed by the existing group runtime.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42, 43)
	d.setModel(t, "spawn")
	f.groupText(1, 42, -100, 7, `{"tool":"group_session","action":"start","title":"Public saved workflow","message":"{\"tool\":\"group_note\",\"text\":\"public workflow note\"}"}`)
	for {
		got := f.next(t)
		if strings.Contains(got.Text, "task-result:") {
			break
		}
	}
	conn := controlWriteDB(t, data)
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
	// Another topic must not list or run it. Another actor cannot borrow its original grant.
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
	// The original sender resumes the same stored child through a signed menu control.
	d.setModel(t, "echo")
	f.callback(6, 42, -100, 7, menu.ID, resume)
	if event := f.callbackResult(t); event.Text != "Accepted." {
		t.Fatalf("saved workflow callback=%+v", event)
	}
	for {
		got := f.next(t)
		if strings.Contains(got.Text, "note-") {
			if got.ChatID != -100 || got.TopicID != 7 {
				t.Fatalf("resumed workflow went to private Main: %+v", got)
			}
			break
		}
	}
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
