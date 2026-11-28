package app_test

import (
	"net/http"
	"strings"
	"testing"
)

const albumWebhookSecret = "album-intake-hook-secret"

func enableAlbumWebhook(t *testing.T, d *dex, f *fakeTelegram) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/bots/default/channels/telegram", map[string]any{
		"allowedUsers": []int64{42}, "transport": "webhook",
		"webhook": map[string]string{"url": "https://hooks.example.test" + telegramHook, "secretToken": albumWebhookSecret},
	}, nil)
	f.nextEvent(t, "setWebhook")
}

func postAlbumMember(t *testing.T, d *dex, update map[string]any) {
	t.Helper()
	postChannelHook(t, d, telegramHook, hookJSON(t, update),
		map[string]string{"X-Telegram-Bot-Api-Secret-Token": albumWebhookSecret}, http.StatusOK)
}

func assertAlbumReply(t *testing.T, reply sentMessage, chat int64, first, second string) {
	t.Helper()
	a := strings.Index(reply.Text, "caption "+first)
	b := strings.Index(reply.Text, "caption "+second)
	if reply.ChatID != chat || a < 0 || b <= a || !strings.Contains(reply.Text, "Telegram album: 2 files.") {
		t.Fatalf("chat %d album with %s then %s: got %+v", chat, first, second, reply)
	}
}

func TestUserTelegramAlbumSpansPollingPages(t *testing.T) {
	t.Parallel()
	// Given the first member is the last update on a full Telegram page.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "one", "two"
	for i := range int64(99) {
		f.updates = append(f.updates, map[string]any{"update_id": i + 1})
	}
	f.albumFiles("two-pages", 100, "one.txt", "two.txt")
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)

	// When polling advances to the second page, both members reach one input in order.
	assertAlbumReply(t, f.next(t), 42, "one.txt", "two.txt")
	eventually(t, "polling acknowledged both pages", func() (bool, any) {
		offset := f.pollOffset.Load()
		return offset == 102, offset
	})
	d.stop()
	conn := controlWriteDB(t, data)
	if inputs, downloads := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'"), f.fileRequests.Load(); inputs != 1 || downloads != 2 {
		t.Fatalf("inputs=%d downloads=%d; want one input and two downloads", inputs, downloads)
	}
}

func TestUserTelegramWebhooksKeepInterleavedAlbumsSeparateAcrossChats(t *testing.T) {
	t.Parallel()
	// Given two private chats with the same group ID. The sender stays the same to isolate the peer key.
	f := newFakeTelegram(t)
	names := []string{"a1.txt", "b1.txt", "a2.txt", "b2.txt"}
	for _, name := range names {
		f.files[name] = name
	}
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	enableAlbumWebhook(t, d, f)
	conn := holdTelegramAlbum(t, d)

	// When the webhook rows arrive in alternating order, each chat gets its own complete input.
	for i, name := range names {
		update := telegramAlbumUpdate("same-group", int64(i+1), name)
		update["message"].(map[string]any)["chat"] = map[string]any{"id": 42 + i%2, "type": "private"}
		postAlbumMember(t, d, update)
	}
	eventually(t, "all four webhook rows finished before either album can seal", func() (bool, any) {
		finished := count(t, conn, "SELECT count(*) FROM channel_webhook_queue WHERE payload IS NULL")
		return finished == 4, finished
	})
	execSQL(t, conn, "DROP TRIGGER hold_album_seal")
	replies := make(map[int64]sentMessage)
	for range 2 {
		reply := f.next(t)
		replies[reply.ChatID] = reply
	}
	assertAlbumReply(t, replies[42], 42, "a1.txt", "a2.txt")
	assertAlbumReply(t, replies[43], 43, "b1.txt", "b2.txt")
	d.stop()
	if inputs, downloads := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'"), f.fileRequests.Load(); inputs != 2 || downloads != 4 {
		t.Fatalf("inputs=%d downloads=%d; want two inputs and four downloads", inputs, downloads)
	}
}

func TestUserTelegramWebhookReplaysCommittedAlbumMemberWithoutRedownload(t *testing.T) {
	t.Parallel()
	// Given the first member commits but the webhook worker cannot mark its row finished.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "one", "two"
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	enableAlbumWebhook(t, d, f)
	conn := holdTelegramAlbum(t, d)
	execSQL(t, conn, `CREATE TRIGGER hold_album_webhook_finish BEFORE UPDATE OF payload ON channel_webhook_queue
WHEN OLD.platform='telegram' AND OLD.event_id='1' AND NEW.payload IS NULL
BEGIN SELECT RAISE(ABORT,'hold webhook finish'); END`)
	postAlbumMember(t, d, telegramAlbumUpdate("replay", 1, "one.txt"))
	postAlbumMember(t, d, telegramAlbumUpdate("replay", 2, "two.txt"))
	eventually(t, "one member committed and both webhook rows still pending", func() (bool, any) {
		stored := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='collecting'")
		pending := count(t, conn, "SELECT count(*) FROM channel_webhook_queue WHERE payload IS NOT NULL")
		return stored == 1 && pending == 2, []int{stored, pending}
	})
	d.stop()
	execSQL(t, conn, "DROP TRIGGER hold_album_webhook_finish")

	// When dex restarts, it skips the stored member's download and then receives the next member.
	d = startDex(t, data, f.srv.URL)
	eventually(t, "replayed webhook rows finished", func() (bool, any) {
		finished := count(t, conn, "SELECT count(*) FROM channel_webhook_queue WHERE payload IS NULL")
		return finished == 2, finished
	})
	execSQL(t, conn, "DROP TRIGGER hold_album_seal")
	assertAlbumReply(t, f.next(t), 42, "one.txt", "two.txt")
	d.stop()
	if inputs, downloads, uploads := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'"), f.fileRequests.Load(), uploadCount(t, data); inputs != 1 || downloads != 2 || uploads != 2 {
		t.Fatalf("inputs=%d downloads=%d uploads=%d; want 1, 2, 2", inputs, downloads, uploads)
	}
}
