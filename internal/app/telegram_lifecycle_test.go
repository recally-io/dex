package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestUserTelegramSettingsStopBeforeWrite(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			// Given a bot with an outstanding long poll and a locked settings database.
			f := newFakeTelegram(t)
			f.pollStarted = make(chan struct{}, 1)
			f.pollCanceled = make(chan struct{}, 1)
			release := make(chan struct{})
			f.pollRelease = release
			dir := t.TempDir()
			d := startTelegram(t, f, dir, 42)
			t.Cleanup(func() { close(release) })
			select {
			case <-f.pollStarted:
			case <-time.After(10 * time.Second):
				t.Fatal("Telegram did not start polling")
			}
			conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			tx, err := conn.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(t.Context(), "UPDATE telegram_settings SET allowed_users=allowed_users"); err != nil {
				t.Fatal(err)
			}

			// When settings are changed while the write is blocked.
			result := make(chan int, 1)
			go func() {
				var body any
				if method == http.MethodPut {
					body = map[string]any{"token": "", "allowedUsers": []int64{42}}
				}
				status, _ := d.call(t, method, "/api/agents/default/channels/telegram", body)
				result <- status
			}()
			// Then the old bot is stopped before the database write can finish.
			stopped := false
			var status int
			finished := false
			select {
			case <-f.pollCanceled:
				stopped = true
			case status = <-result:
				finished = true
			case <-time.After(3 * time.Second):
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if !finished {
				select {
				case status = <-result:
				case <-time.After(10 * time.Second):
					t.Fatal("settings did not finish")
				}
			}
			if !stopped {
				t.Fatal("settings waited for the database before stopping Telegram")
			}
			if status != http.StatusOK {
				t.Fatalf("settings status = %d", status)
			}
		})
	}
}

func TestUserTelegramContinuesAfterSettingsWriteFails(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			// Given an active bot and a database that rejects the requested write.
			f := newFakeTelegram(t)
			dir := t.TempDir()
			d := startTelegram(t, f, dir, 42)
			conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			op := "UPDATE"
			if method == http.MethodDelete {
				op = "DELETE"
			}
			if _, err := conn.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER reject_telegram BEFORE %s ON telegram_settings BEGIN SELECT RAISE(ABORT, 'test failure'); END", op)); err != nil {
				t.Fatal(err)
			}
			var body any
			if method == http.MethodPut {
				body = map[string]any{"token": "", "allowedUsers": []int64{42}}
			}
			// When the settings operation fails.
			status, _ := d.call(t, method, "/api/agents/default/channels/telegram", body)
			if status < 400 {
				t.Fatalf("settings status = %d", status)
			}
			// Then the saved bot can still respond to its owner.
			f.message(1, 42, 7, "after failure")
			if got := f.next(t); got.Text != "echo: after failure" {
				t.Fatalf("reply = %+v", got)
			}
		})
	}
}
