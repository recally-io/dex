package app_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

func TestUserCanRestartAfterStartupFailure(t *testing.T) {
	for _, failure := range []string{"schedule", "saved channel"} {
		t.Run(failure, func(t *testing.T) {
			// Given a saved Telegram bot and an invalid startup input.
			f := newFakeTelegram(t)
			data, home := t.TempDir(), t.TempDir()
			first := startTelegram(t, f, data, 42)
			conn := sessionDB(t, first)
			first.stop()
			cfg := app.Config{DataDir: data, Home: home, ToolEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home}, FakeModel: true, TelegramAPIURL: f.srv.URL}
			if failure == "schedule" {
				cfg.DreamEvery = -time.Second
			} else {
				if _, err := conn.ExecContext(t.Context(), "UPDATE telegram_settings SET allowed_users='invalid'"); err != nil {
					t.Fatal(err)
				}
			}
			// When startup fails after construction, it retains the cause and closes its resources.
			failed, err := app.Start(t.Context(), cfg)
			if failed != nil {
				_ = failed.Close()
				t.Fatal("invalid startup succeeded")
			}
			if err == nil {
				t.Fatal("startup lost its error")
			}
			cause := "schedule dream"
			if failure == "saved channel" {
				cause = "telegram_settings.allowed_users"
			}
			if !strings.Contains(err.Error(), cause) {
				t.Fatalf("startup error=%v; want %s", err, cause)
			}
			if strings.Contains(err.Error(), "database is closed") {
				t.Fatalf("startup closed storage twice: %v", err)
			}
			// Then correcting the input permits a restart with the saved bot and native execution.
			if _, err := conn.ExecContext(t.Context(), "UPDATE telegram_settings SET allowed_users='42'"); err != nil {
				t.Fatal(err)
			}
			restarted := startDex(t, data, f.srv.URL)
			f.message(1, 42, 7, "after startup failure")
			if got := f.next(t); got.Text != "echo: after startup failure" {
				t.Fatalf("restart reply=%q", got.Text)
			}
			restarted.must(t, http.MethodGet, "/api/health", nil, nil)
			t.Logf("artifact: %s startup failure retained cause; corrected app reopened SQLite and delivered one native answer", failure)
		})
	}
}
