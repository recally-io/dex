package agent

import (
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserPairingRetriesWhileChannelSettingsChange(t *testing.T) {
	// Given settings being changed while an old channel runtime is stopping.
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err = q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO telegram_settings (agent_id,token,allowed_users,pairing_code,pairing_expires) VALUES ('a','123:secret','','code',9999999999)`,
		`INSERT INTO feishu_settings (agent_id,app_id,app_secret,brand,owner_id,pairing_code,pairing_expires_at) VALUES ('a','app','secret','feishu','','code',9999999999999)`,
	} {
		if _, err = conn.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	s := New(q, nil, "", nil)
	s.mu.Lock()
	// When a platform delivers a pairing request during the change.
	_, tgErr := s.pairTelegram("a", "123:secret")(t.Context(), "code", 42)
	_, fsErr := s.PairFeishu(t.Context(), "a", "owner", "code")
	s.mu.Unlock()
	// Then each platform receives a retryable error without waiting for the stopping runtime.
	if tgErr == nil || fsErr == nil {
		t.Fatalf("telegram error=%v feishu error=%v", tgErr, fsErr)
	}
}
