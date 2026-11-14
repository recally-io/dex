package app_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (f *fakeTelegram) groupText(updateID, sender, chat, topic int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_id": updateID, "message": map[string]any{
		"message_id": updateID, "from": map[string]any{"id": sender}, "chat": map[string]any{"id": chat, "type": "supergroup", "is_forum": topic > 0}, "message_thread_id": topic, "text": text,
		"reply_to_message": map[string]any{"message_id": 900, "from": map[string]any{"id": 123, "is_bot": true}},
	}})
}

func (f *fakeTelegram) groupMedia(updateID, sender, chat, topic int64, kind, file string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_id": updateID, "message": map[string]any{"message_id": updateID, "from": map[string]any{"id": sender}, "chat": map[string]any{"id": chat, "type": "supergroup", "is_forum": topic > 0}, "message_thread_id": topic, "caption": "public group attachment", kind: map[string]any{"file_id": file, "file_name": file, "duration": 3}, "reply_to_message": map[string]any{"message_id": 900, "from": map[string]any{"id": 123, "is_bot": true}}}})
}

func TestUserTelegramGroupTopicCannotReadPrivateMain(t *testing.T) {
	// Given a private Main that already contains private conversation context.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "context")
	f.message(1, 42, 7, "PRIVATE_MAIN_CONTEXT_CANARY")
	if got := f.next(t); !strings.Contains(got.Text, "PRIVATE_MAIN_CONTEXT_CANARY") {
		t.Fatalf("private precondition: %+v", got)
	}
	// When the paired actor replies to the bot inside a group topic.
	f.groupText(2, 42, -100, 7, "PUBLIC_GROUP_CONTEXT_CANARY")
	got := f.next(t)
	// Then the reply stays in that topic and its model context cannot fall back to Main.
	if got.ChatID != -100 || got.TopicID != 7 || !strings.Contains(got.Text, "PUBLIC_GROUP_CONTEXT_CANARY") || strings.Contains(got.Text, "PRIVATE_MAIN_CONTEXT_CANARY") {
		t.Fatalf("group route/context leaked: %+v", got)
	}
	f.groupText(3, 42, -200, 7, "OTHER_GROUP_CONTEXT_CANARY")
	if other := f.next(t); other.ChatID != -200 || strings.Contains(other.Text, "PUBLIC_GROUP_CONTEXT_CANARY") || strings.Contains(other.Text, "PRIVATE_MAIN_CONTEXT_CANARY") {
		t.Fatalf("chat audiences mixed: %+v", other)
	}
}

func TestUserTelegramGroupClearStaysInCurrentTopic(t *testing.T) {
	// Given private Main and two topic conversations with distinct context.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "context")
	f.message(1, 42, 7, "PRIVATE_MAIN_KEPT")
	f.next(t)
	f.groupText(2, 42, -100, 7, "TOPIC_SEVEN_OLD")
	f.next(t)
	f.groupText(3, 42, -100, 8, "TOPIC_EIGHT_KEPT")
	f.next(t)
	// When a group sender clears the current topic, without any owner authority.
	f.groupText(4, 42, -100, 7, "/clear")
	if got := f.next(t); got.ChatID != -100 || got.TopicID != 7 || got.Text != "Context cleared." {
		t.Fatalf("scope clear: %+v", got)
	}
	// Then only that topic loses context; another topic and private Main retain theirs.
	f.groupText(5, 42, -100, 7, "TOPIC_SEVEN_NEW")
	if got := f.next(t); strings.Contains(got.Text, "TOPIC_SEVEN_OLD") || strings.Contains(got.Text, "PRIVATE_MAIN_KEPT") || !strings.Contains(got.Text, "TOPIC_SEVEN_NEW") {
		t.Fatalf("cleared topic: %+v", got)
	}
	f.groupText(6, 42, -100, 8, "TOPIC_EIGHT_NEW")
	if got := f.next(t); !strings.Contains(got.Text, "TOPIC_EIGHT_KEPT") || strings.Contains(got.Text, "TOPIC_SEVEN_") {
		t.Fatalf("other topic: %+v", got)
	}
	f.message(7, 42, 7, "PRIVATE_MAIN_NEW")
	if got := f.next(t); !strings.Contains(got.Text, "PRIVATE_MAIN_KEPT") || strings.Contains(got.Text, "TOPIC_") {
		t.Fatalf("private Main changed: %+v", got)
	}
}

func TestUserTelegramGroupChecksCurrentPairingBeforeDownloadAndPublishesOnlyInAudience(t *testing.T) {
	// Given a running transport whose cached actor list is older than the control settings.
	directory := t.TempDir()
	f := newFakeTelegram(t)
	f.files["group.txt"] = "PUBLIC_GROUP_FILE_BODY"
	startTelegram(t, f, directory, 42)
	conn := controlWriteDB(t, directory)
	execSQL(t, conn, "UPDATE telegram_settings SET allowed_users='43' WHERE agent_id='default'")
	// When the revoked actor sends a triggered attachment, no getFile request or upload may start.
	f.groupMedia(1, 42, -100, 7, "document", "group.txt")
	eventually(t, "revoked group update is acknowledged without download", func() (bool, any) {
		return f.pollOffset.Load() >= 2 || f.fileRequests.Load() > 0, []int64{f.pollOffset.Load(), f.fileRequests.Load()}
	})
	if f.fileRequests.Load() != 0 || uploadCount(t, directory) != 0 {
		t.Fatalf("revoked actor downloaded: requests=%d", f.fileRequests.Load())
	}
	// When the same actor is currently authorized, the file belongs only to this bot/chat/topic audience.
	execSQL(t, conn, "UPDATE telegram_settings SET allowed_users='42' WHERE agent_id='default'")
	f.groupMedia(2, 42, -100, 7, "document", "group.txt")
	got := f.next(t)
	if got.ChatID != -100 || got.TopicID != 7 || !strings.Contains(got.Text, "group.txt") {
		t.Fatalf("group file input: %+v", got)
	}
	files, err := filepath.Glob(filepath.Join(directory, "agents", "default", "telegram-groups", "tg_123_-100_7", "resources", "uploads", "*", "group.txt"))
	if err != nil || len(files) != 1 || uploadCount(t, directory) != 0 {
		t.Fatalf("wrong upload ownership: group=%v private=%d err=%v", files, uploadCount(t, directory), err)
	}
	body, err := os.ReadFile(files[0])
	if err != nil || string(body) != "PUBLIC_GROUP_FILE_BODY" {
		t.Fatalf("published file=%q err=%v", body, err)
	}
}

func TestUserTelegramGroupAudioDoesNotBorrowPrivateSpeechConsent(t *testing.T) {
	// Given explicit STT consent for private Telegram, with a deterministic local provider.
	directory := t.TempDir()
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadpublic"
	d := startTelegram(t, f, directory, 42)
	boundary, calls := localSTT(t, "PRIVATE_STT_MUST_NOT_APPLY")
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "explicit-stt"}, nil)
	// When a paired actor sends group audio, retain the original file without a speech request.
	f.groupMedia(1, 42, -100, 7, "voice", "voice.ogg")
	got := f.next(t)
	if calls.Load() != 0 || strings.Contains(got.Text, "PRIVATE_STT_MUST_NOT_APPLY") || got.ChatID != -100 {
		t.Fatalf("private speech consent leaked: calls=%d reply=%+v", calls.Load(), got)
	}
	files, err := filepath.Glob(filepath.Join(directory, "agents", "default", "telegram-groups", "tg_123_-100_7", "resources", "uploads", "*", "voice.ogg"))
	if err != nil || len(files) != 1 {
		t.Fatalf("original group audio missing: files=%v err=%v", files, err)
	}
}

func TestUserTelegramGroupStopCannotStopMainOrAnotherTopic(t *testing.T) {
	// Given long native model streams in Main and two independent topic audiences.
	directory := t.TempDir()
	f := newFakeTelegram(t)
	startTelegram(t, f, directory, 42)
	conn := controlWriteDB(t, directory)
	f.message(1, 42, 7, strings.Repeat("private ", 128))
	f.groupText(2, 42, -100, 7, strings.Repeat("current ", 1024))
	f.groupText(3, 42, -100, 8, strings.Repeat("other ", 128))
	eventually(t, "all three native inputs admitted", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE status='pending' AND admitted_at IS NOT NULL")
		return n == 3, n
	})
	// When the actor stops only the current group topic.
	f.groupText(4, 42, -100, 7, "/stop")
	eventually(t, "current topic stopped", func() (bool, any) {
		var status, reason string
		err := conn.QueryRowContext(t.Context(), "SELECT status,coalesce(reason,'') FROM submissions WHERE source_key='telegram:123:2'").Scan(&status, &reason)
		return err == nil && status == "unanswered" && reason == "stopped", fmt.Sprint(status, " ", reason, " ", err)
	})
	// Then Main and the other topic still finish successfully rather than receiving a native abort.
	eventually(t, "unrelated native inputs finish", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key IN ('telegram:123:1','telegram:123:3') AND status='done'")
		return n == 2, n
	})
}

func TestUserTelegramGroupTriggerAndGeneralTopicKeepSessionRules(t *testing.T) {
	// Given paired group actors and a context-sensitive model.
	directory := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, directory, 42, 43)
	d.setModel(t, "context")
	conn := controlWriteDB(t, directory)
	appendText := func(id, sender, chat int64, forum, reply bool, text string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		message := map[string]any{"message_id": id, "from": map[string]any{"id": sender}, "chat": map[string]any{"id": chat, "type": "supergroup", "is_forum": forum}, "text": text}
		if reply {
			message["reply_to_message"] = map[string]any{"message_id": 900, "from": map[string]any{"id": 123, "is_bot": true}}
		}
		f.updates = append(f.updates, map[string]any{"update_id": id, "message": message})
	}
	// When an ordinary group message neither mentions nor replies to this bot, it cannot trigger work.
	appendText(1, 42, -100, false, false, "UNTRIGGERED_GROUP_TEXT")
	eventually(t, "untriggered group update acknowledged", func() (bool, any) { return f.pollOffset.Load() >= 2, f.pollOffset.Load() })
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1'"); n != 0 {
		t.Fatalf("untriggered input created %d receipts", n)
	}
	// Mention-triggered ordinary groups keep a separate model session for each sender.
	appendText(2, 42, -100, false, false, "@dex_test_bot ORDINARY_ACTOR_42")
	if got := f.next(t); !strings.Contains(got.Text, "ORDINARY_ACTOR_42") || strings.Contains(got.Text, "@dex_test_bot") {
		t.Fatalf("own mention was not parsed: %+v", got)
	}
	f.groupText(3, 43, -100, 0, "ORDINARY_ACTOR_43")
	if got := f.next(t); strings.Contains(got.Text, "ORDINARY_ACTOR_42") || !strings.Contains(got.Text, "ORDINARY_ACTOR_43") {
		t.Fatalf("ordinary sender sessions mixed: %+v", got)
	}
	// A forum General topic has no thread ID, but still shares only its own topic session.
	appendText(4, 42, -200, true, true, "GENERAL_ACTOR_42")
	f.next(t)
	appendText(5, 43, -200, true, true, "GENERAL_ACTOR_43")
	if got := f.next(t); !strings.Contains(got.Text, "GENERAL_ACTOR_42") || !strings.Contains(got.Text, "GENERAL_ACTOR_43") || strings.Contains(got.Text, "ORDINARY_ACTOR_") {
		t.Fatalf("General topic session rule lost: %+v", got)
	}
}
