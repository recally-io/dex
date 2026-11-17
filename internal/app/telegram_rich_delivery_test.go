package app_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestUserRichTelegramRetriesKeepTheFrozenMethod(t *testing.T) {
	for name, failure := range map[string]platformFailure{"flood": {Code: 429, Retry: 1}, "unknown HTTP": {HTTPStatus: http.StatusServiceUnavailable}, "unknown ID": {ShortResult: true}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Given a temporary or unknown response to a native rich send.
			f := newFakeTelegram(t)
			f.failures["sendRichMessage"] = []platformFailure{failure}
			startTelegram(t, f, t.TempDir(), 42)
			// When the durable sender retries, it must keep the frozen method and body.
			f.message(1, 42, 42, "retry **rich**")
			got := f.next(t)
			if got.RichHTML != "<p>echo: retry <b>rich</b></p>" {
				t.Fatalf("retry payload=%+v", got)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			sends := 0
			for _, request := range f.requests {
				if request.Method == "sendRichMessageDraft" {
					continue
				}
				if request.Method != "sendRichMessage" || request.RichHTML != got.RichHTML {
					t.Fatalf("retry switched method or payload: %+v", request)
				}
				sends++
			}
			if sends != 2 {
				t.Fatalf("send attempts=%d", sends)
			}
		})
	}
}

func TestUserRichTelegramReplyKeepsFrozenPartsAcrossRestart(t *testing.T) {
	t.Parallel()
	// Given a two-part rich reply whose first part is confirmed.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failAfter.Store(1)
	d := startTelegram(t, f, data, 42)
	setAnswer := useRichAnswerProvider(t, d)
	answer := strings.Repeat("😀x", 20000) + "FROZEN-END"
	setAnswer(answer)
	f.message(1, 42, 42, "generate long reply")
	first := f.next(t)
	firstText := richPayloadText(t, first)
	conn := controlWriteDB(t, data)
	var frozen string
	eventually(t, "first rich part committed and remainder pending", func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT rendered_reply FROM submissions WHERE source_key='telegram:123:1' AND delivered_parts=1 AND delivered_at IS NULL AND channel_retry_at>0").Scan(&frozen)
		return err == nil, frozen
	})
	d.stop()
	// When the answer text changes after settlement, recovery still uses the frozen payload.
	execSQL(t, conn, "UPDATE submissions SET answer='must not be re-rendered' WHERE source_key='telegram:123:1'")
	f.failAfter.Store(0)
	startDex(t, data, f.srv.URL)
	second := f.next(t)
	secondText := richPayloadText(t, second)
	if firstText+secondText != answer {
		t.Fatal("restart re-rendered, repeated, or lost rich text")
	}
	eventually(t, "frozen rich reply delivered", func() (bool, any) {
		return count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND rendered_reply=?", frozen) == 1, nil
	})
}

func TestUserTelegramRichInputKeepsBodyWithoutExecutingRichCommands(t *testing.T) {
	t.Parallel()
	// Given rich message content from an authorized sender.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "rich_message": map[string]any{"blocks": []any{
		map[string]any{"type": "paragraph", "text": []any{"/clear 中文 ", map[string]any{"type": "bold", "text": "kept"}}},
		map[string]any{"type": "table", "cells": []any{[]any{map[string]any{"text": "Header"}}, []any{map[string]any{"text": "Value😀"}}}},
	}}}})
	f.mu.Unlock()
	// Then the body enters native execution as content, not a slash command.
	got := f.next(t)
	for _, text := range []string{"echo: Rich message:", "/clear 中文 kept", "Header", "Value😀"} {
		if !strings.Contains(got.RichHTML, text) {
			t.Fatalf("rich body lost %q: %+v", text, got)
		}
	}
	// Invalid over-nesting has an explicit bounded-content note, not a recursive walk.
	var deep any = "hidden"
	for range 70 {
		deep = map[string]any{"type": "bold", "text": deep}
	}
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(2), "message": map[string]any{"message_id": 2, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "rich_message": map[string]any{"blocks": []any{map[string]any{"type": "paragraph", "text": deep}}}}})
	f.mu.Unlock()
	if bounded := f.next(t); !strings.Contains(bounded.RichHTML, "Rich nesting limit reached") {
		t.Fatalf("missing rich input limit note: %+v", bounded)
	}
}
