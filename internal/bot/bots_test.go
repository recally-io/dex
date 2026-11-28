package bot_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserSavesSettingsForEachBot(t *testing.T) {
	// Given two bots with no model, which need no worker configuration.
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	s := bot.New(q, conn, new(sync.Mutex), nil, "", nil)
	for _, name := range []string{"First", "Second"} {
		if _, err := s.CreateBot(t.Context(), bot.Patch{Name: &name}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := q.ListBots(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("bots=%+v err=%v", rows, err)
	}
	// When the owner changes the second bot.
	if err := s.SaveBot(t.Context(), rows[1].ID, bot.Bot{Name: " Changed ", ThinkingLevel: "high", Instructions: "Second only"}); err != nil {
		t.Fatal(err)
	}
	// Then the first bot keeps its settings.
	first, err := s.Bot(t.Context(), rows[0].ID)
	if err != nil || first.Name != "First" || first.Instructions != "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := s.Bot(t.Context(), rows[1].ID)
	if err != nil || second.Name != "Changed" || second.Instructions != "Second only" || second.ThinkingLevel != "high" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := s.CreateBot(t.Context(), bot.Patch{Name: new(" ")}); !errors.Is(err, bot.ErrInvalid) {
		t.Fatalf("invalid name: %v", err)
	}
	listed, err := s.ListBots(t.Context())
	if err != nil || len(listed) != 2 {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	t.Logf("artifact: bot IDs=%s,%s; second=%+v", rows[0].ID, rows[1].ID, second)
}
