package dream

import (
	"database/sql"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestCutKeepsStartAndEndOfLongText(t *testing.T) {
	text := strings.Repeat("记", maxHead) + strings.Repeat("x", 10) + strings.Repeat("忆", maxTail)
	got, ok := cut(text)
	if !ok || !strings.HasPrefix(got, strings.Repeat("记", maxHead)+"\n[…]\n") ||
		!strings.HasSuffix(got, strings.Repeat("忆", maxTail)) || !utf8.ValidString(got) {
		t.Fatalf("cut = %v, %q…", ok, got[:40])
	}
	if got, ok := cut(text[:len(text)/2]); ok || got != text[:len(text)/2] {
		t.Fatal("cut a short text")
	}
}

func TestDigestStopsAtByteLimitAndKeepsOneRecord(t *testing.T) {
	long := strings.Repeat("a", maxHead+maxTail)
	rows := make([]sqlc.ListDreamInputRow, 40)
	for i := range rows {
		rows[i] = sqlc.ListDreamInputRow{
			ID: string(rune('a' + i)), ChatID: 1, Content: long, Status: "done",
			Answer: sql.NullString{String: long, Valid: true},
		}
	}
	text, last, err := digest("default", rows)
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(text, "\n{")
	if len(text) > maxBytes || n == 0 || n == len(rows) || last.ID != rows[n-1].ID {
		t.Fatalf("%d bytes, %d records, last %q", len(text), n, last.ID)
	}
}
