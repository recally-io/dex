package bot_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCannotSaveTelegramSettingsAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given saved Telegram credentials and a request to replace them.
	q, writer, id := archiveRaceDB(t)
	if _, err := q.SetTelegramSettings(t.Context(), sqlc.SetTelegramSettingsParams{BotID: id, Token: "123:old", Options: "{}"}); err != nil {
		t.Fatal(err)
	}
	var started bool
	var archiveErr error
	s := bot.New(sqlc.New(writer), writer, new(sync.Mutex), nil, "", func(_ string, cfg *telegram.Config) {
		if cfg != nil {
			started = true
			return
		}
		// Commit the archive as the old account stops, before the settings write.
		archiveErr = db.ArchiveBot(t.Context(), q, id, time.Now())
	})

	// When the save continues after the archive commits.
	saveErr := s.SetTelegram(t.Context(), id, "456:new", nil, nil, "", nil)
	if archiveErr != nil {
		t.Fatal(archiveErr)
	}

	// Then the old credentials remain and no runtime starts.
	cfg, err := s.TelegramConfig(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	kept := cfg != nil && cfg.Token == "123:old"
	if !errors.Is(saveErr, db.ErrBotArchived) || !kept || started {
		t.Fatalf("after archive: err=%v old credentials kept=%t start requested=%t", saveErr, kept, started)
	}
}

func TestUserCannotPairTelegramAfterArchiveCommit(t *testing.T) {
	t.Parallel()
	// Given an archived bot with still-valid a pairing code and a runtime that has not stopped yet.
	q, writer, id := archiveRaceDB(t)
	ctx := t.Context()
	if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: id, Token: "123:secret", Options: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetTelegramPairing(ctx, sqlc.SetTelegramPairingParams{BotID: id, PairingCode: "code", PairingExpires: time.Now().Add(time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	s := bot.New(q, writer, new(sync.Mutex), nil, "", nil)
	tg, err := s.TelegramConfig(ctx, id)
	if err != nil || tg == nil {
		t.Fatalf("read initial Telegram settings: configured=%t err=%v", tg != nil, err)
	}
	if err := db.ArchiveBot(ctx, q, id, time.Now()); err != nil {
		t.Fatal(err)
	}

	// When the runtime delivers a pairing request, the archived bot must not gain an owner.
	users, telegramErr := tg.Pair(ctx, "code", 42)
	telegramRow, err := q.GetTelegramSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 || !errors.Is(telegramErr, db.ErrBotArchived) || telegramRow.AllowedUsers != "" {
		t.Fatalf("after archive: Telegram users=%v saved=%q err=%v", users, telegramRow.AllowedUsers, telegramErr)
	}
}

func archiveRaceDB(t *testing.T) (*sqlc.Queries, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	open := func() *sql.DB {
		conn, err := db.Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	q := sqlc.New(open())
	const id = "coder"
	if err := q.CreateBotIfMissing(t.Context(), sqlc.CreateBotIfMissingParams{ID: id, Name: "Coder"}); err != nil {
		t.Fatal(err)
	}
	writer := open()
	writer.SetMaxOpenConns(1)
	return q, writer, id
}
