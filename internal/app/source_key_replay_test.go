package app_test

import (
	"slices"
	"testing"
)

func TestUserChannelSourceReplayDoesNotDuplicateInput(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "echo")
	f.message(1, 42, 7, "once")
	f.message(1, 42, 7, "once")
	if got := f.next(t); got.Text != "echo: once" {
		t.Fatalf("reply = %+v", got)
	}
	f.message(2, 42, 7, "barrier")
	if got := f.next(t); got.Text != "echo: barrier" {
		t.Fatalf("duplicate reply or barrier = %+v", got)
	}
	conn := sessionDB(t, d)
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipts = %d, error = %v", count, err)
	}
	w := d.watch(t, d.mainChat(t))
	w.until(t, "one channel input", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "user" && it.Text == "barrier" })
	})
	count = 0
	for _, it := range w.tr.items {
		if it.Kind == "user" && it.Text == "once" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("channel inputs = %d", count)
	}
}
