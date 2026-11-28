package app_test

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCanSelectOnlyTheirAuthorizedChannelTarget(t *testing.T) {
	t.Parallel()
	// Given two bots, only one with a paired Feishu owner.
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	for _, id := range []string{"a", "b"} {
		if err = q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.ExecContext(t.Context(), `INSERT INTO feishu_settings (bot_id, app_id, app_secret, brand, owner_id) VALUES ('a','app','test-secret','feishu','owner')`); err != nil {
		t.Fatal(err)
	}
	mu := new(sync.Mutex)
	s := bot.New(q, conn, mu, nil)
	s.BindFeishu(feishu.NewSetup(q, conn, mu, "", "", nil, nil))
	// When the user lists eligible targets and selects one as primary.
	list, err := s.NotificationTargets(t.Context(), "a")
	if err != nil || len(list.Targets) != 1 {
		t.Fatalf("targets=%+v err=%v", list, err)
	}
	id := list.Targets[0].ID
	if err = s.SetPrimaryNotificationTarget(t.Context(), "a", id); err != nil {
		t.Fatal(err)
	}
	// Then another bot cannot use that target and the owner sees it as primary.
	if err = s.SetPrimaryNotificationTarget(t.Context(), "b", id); err == nil {
		t.Fatal("accepted another bot's target")
	}
	list, err = s.NotificationTargets(t.Context(), "a")
	if err != nil || list.PrimaryID != id {
		t.Fatalf("primary=%+v err=%v", list, err)
	}
}
