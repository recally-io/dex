package app_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestUserTelegramAlbumOverflowKeepsTenFilesPerNativeInput(t *testing.T) {
	t.Parallel()
	// Given more than ten members, including a member that can arrive after a batch is sealed.
	f := newFakeTelegram(t)
	var names []string
	for i := range 11 {
		name := fmt.Sprintf("part-%02d.txt", i)
		names = append(names, name)
		f.files[name] = "content"
	}
	d := startTelegram(t, f, t.TempDir(), 42)
	f.albumFiles("large-album", 1, names...)
	// When the input limit seals the first batch, every member still reaches one of the two native inputs.
	watch := d.watch(t, d.mainChat(t))
	watch.until(t, "eleven files in bounded native inputs", func(tr *transcript) bool {
		counts := []int{}
		total := 0
		for _, item := range tr.items {
			if item.Kind != "user" {
				continue
			}
			counts = append(counts, len(item.Attachments))
			total += len(item.Attachments)
		}
		return len(counts) == 2 && counts[0] == 10 && counts[1] == 1 && total == 11
	})
}

func TestUserStopDiscardsSealedTelegramAlbumAwaitingReceipt(t *testing.T) {
	t.Parallel()
	// Given a sealed album whose business receipt fails before native admission.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "one", "two"
	d := startTelegram(t, f, t.TempDir(), 42)
	conn := controlWriteDB(t, d.dataDir(t))
	execSQL(t, conn, "CREATE TRIGGER hold_album_input BEFORE INSERT ON submissions WHEN NEW.source_key LIKE 'telegram:%:album:%' BEGIN SELECT RAISE(ABORT,'hold album input'); END")
	f.albumFiles("sealed-album", 1, "one.txt", "two.txt")
	eventually(t, "frozen album with no native input", func() (bool, any) {
		sealed := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='sealed'")
		inputs := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'")
		return sealed == 2 && inputs == 0, []int{sealed, inputs}
	})
	// When Stop cancels the current scope, a later retry cannot admit the old frozen payload.
	f.message(3, 42, 42, "/stop")
	if got := f.next(t).Text; !strings.Contains(got, "Stop requested") {
		t.Fatalf("stop=%q", got)
	}
	eventually(t, "sealed upload cleanup", func() (bool, any) { n := uploadCount(t, d.dataDir(t)); return n == 0, n })
	execSQL(t, conn, "DROP TRIGGER hold_album_input")
	f.message(4, 42, 42, "after sealed stop")
	if got := f.next(t).Text; got != "echo: after sealed stop" {
		t.Fatalf("cancelled album admitted: %q", got)
	}
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'"); n != 1 {
		t.Fatalf("inputs=%d", n)
	}
}
