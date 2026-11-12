package chat

import "testing"

func TestUserChannelSourceReplayWithNewRequestID(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	ctx := t.Context()
	for range 2 {
		if err := r.service.SubmitMessage(ctx, "bot", "telegram:7", "telegram:123:1", Message{Content: "once"}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := r.conn.QueryRowContext(ctx, "SELECT count(*) FROM submissions WHERE source_key = ?", "telegram:123:1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("source receipts = %d, error = %v", count, err)
	}
}
