package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/agent"
)

func TestUserBlockedControlReplyDoesNotBlockOwner(t *testing.T) {
	t.Parallel()
	for _, attachment := range []bool{false, true} {
		t.Run(fmt.Sprintf("attachment=%v", attachment), func(t *testing.T) {
			// Given an owner and a peer that rejects control replies.
			f := newFakeTelegram(t)
			f.permanentSession.Store(8)
			startTelegram(t, f, t.TempDir(), 42)
			// When an invalid pairing request or attachment rejection precedes an owner message.
			if attachment {
				f.mu.Lock()
				f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
					"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 8, "type": "private"},
					"document": map[string]any{"file_id": "large", "file_name": "large.bin", "file_size": (20 << 20) + 1},
				}})
				f.mu.Unlock()
			} else {
				f.message(1, 99, 8, "/start wrong")
			}
			f.message(2, 42, 7, "still here")
			// Then the owner receives an answer.
			if got := f.next(t); got.ChatID != 7 || got.Text != "echo: still here" {
				t.Fatalf("owner answer = %+v", got)
			}
		})
	}
}

func TestUserCannotPairSecondTelegramOwner(t *testing.T) {
	t.Parallel()
	// Given a bot with an owner.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	// When someone requests another pairing link.
	status, raw := d.call(t, http.MethodPost, "/api/agents/default/channels/telegram/pairing", nil)
	// Then link creation is rejected.
	if status != http.StatusBadRequest {
		t.Fatalf("pairing status = %d: %s", status, raw)
	}
	// When an old valid code remains in storage after an owner was set.
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "UPDATE telegram_settings SET pairing_code='stale', pairing_expires=unixepoch()+600"); err != nil {
		t.Fatal(err)
	}
	f.message(1, 99, 8, "/start stale")
	// Then atomic consumption rejects the second user and keeps the original allowlist.
	eventually(t, "poller handled stale code", func() (bool, any) {
		offset := f.pollOffset.Load()
		return offset >= 2, offset
	})
	assertTelegramOwner(t, d)
}

func assertTelegramOwner(t *testing.T, d *dex) {
	t.Helper()
	var got struct {
		Telegram agent.Telegram `json:"telegram"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/channels", nil, &got)
	if !slices.Equal(got.Telegram.AllowedUsers, []int64{42}) {
		t.Fatalf("allowed users = %v", got.Telegram.AllowedUsers)
	}
}

func TestUserTelegramPairingAcknowledgementSurvivesFailure(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			// Given an unpaired bot and a valid link.
			f := newFakeTelegram(t)
			dir := t.TempDir()
			d := startTelegram(t, f, dir)
			var pair struct {
				URL string `json:"url"`
			}
			d.must(t, http.MethodPost, "/api/agents/default/channels/telegram/pairing", nil, &pair)
			link, err := url.Parse(pair.URL)
			if err != nil {
				t.Fatal(err)
			}
			f.failSend.Store(true)
			f.message(1, 42, 7, "/start "+link.Query().Get("start"))
			// When consumption succeeds but its acknowledgement fails.
			conn := controlWriteDB(t, dir)
			eventually(t, "pairing acknowledgement waits for retry", func() (bool, any) {
				n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE kind = 'control' AND status = 'pending' AND attempts > 0`)
				return n == 1, n
			})
			assertTelegramOwner(t, d)
			if restart {
				d.stop()
				d = startDex(t, dir, f.srv.URL)
			}
			f.failSend.Store(false)
			// Then replay reports access accurately, including after a restart.
			got := f.next(t)
			if !strings.Contains(got.Text, "is paired") {
				t.Fatalf("replayed acknowledgement = %+v", got)
			}
			assertTelegramOwner(t, d)
			f.message(2, 42, 7, "paired")
			if got := f.next(t); got.Text != "echo: paired" {
				t.Fatalf("owner answer = %+v", got)
			}
		})
	}
}

func TestUserGetsTelegramAnswerAfterStartupFailure(t *testing.T) {
	t.Parallel()
	// Given a temporary startup failure and a failing first poll.
	f := newFakeTelegram(t)
	f.failures["getMe"] = []platformFailure{{Code: 500}}
	f.failures["getUpdates"] = []platformFailure{{Code: 500}}
	d := startTelegram(t, f, t.TempDir(), 42)
	waitTelegramState(t, d, "error")
	// When a later message arrives.
	f.message(1, 42, 7, "recovered")
	// Then polling recovers and the sender delivers the answer.
	if got := f.next(t); got.Text != "echo: recovered" {
		t.Fatalf("recovered answer = %+v", got)
	}
	waitTelegramState(t, d, "running")
}
