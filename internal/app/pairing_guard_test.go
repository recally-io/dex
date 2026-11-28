package app_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserPairingRetriesWhileChannelSettingsChange(t *testing.T) {
	t.Parallel()
	// Given valid pairing codes while a settings change holds the shared lock.
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err = q.CreateBotIfMissing(ctx, sqlc.CreateBotIfMissingParams{ID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO telegram_settings (bot_id,token,allowed_users,pairing_code,pairing_expires) VALUES ('a','123:secret','','code',9999999999)`,
		`INSERT INTO feishu_settings (bot_id,app_id,app_secret,brand,owner_id,pairing_code,pairing_expires_at) VALUES ('a','app','secret','feishu','','code',9999999999999)`,
	} {
		if _, err = conn.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	mu := new(sync.Mutex)
	s := telegram.NewSetup(q, conn, mu, "", nil, nil, nil)
	fs := feishu.NewSetup(q, conn, mu, "", "", nil, nil)
	tg, err := s.Config(ctx, "a")
	if err != nil || tg == nil {
		t.Fatalf("cannot load Telegram configuration: configured=%t err=%v", tg != nil, err)
	}
	mu.Lock()
	defer mu.Unlock()

	// When the runtimes deliver pairing requests, then both receive retry errors without waiting for the lock.
	telegramResult, feishuResult := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := tg.Pair(ctx, "code", 42)
		telegramResult <- err
	}()
	go func() {
		_, err := fs.Pair(ctx, "a", "owner", "code")
		feishuResult <- err
	}()
	timeout := time.After(10 * time.Second)
	for _, result := range []struct {
		platform string
		ch       <-chan error
	}{{"Telegram", telegramResult}, {"Feishu", feishuResult}} {
		select {
		case err := <-result.ch:
			if err == nil || err.Error() != "channel settings are changing; retry pairing" {
				t.Errorf("%s did not return a retry error: %v", result.platform, err)
			}
		case <-timeout:
			t.Fatalf("%s pairing waited for the channel settings lock", result.platform)
		}
	}
}
