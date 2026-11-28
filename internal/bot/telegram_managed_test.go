package bot_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/vault"
)

func TestUserDoesNotKeepAnUncommittedTelegramManagerCredential(t *testing.T) {
	t.Parallel()
	// Given an active manager and a replacement that the provider accepts.
	q, conn, _ := archiveRaceDB(t)
	ctx := t.Context()
	store := vault.New(t.TempDir())
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":456,"username":"DexManagerBot","is_bot":true,"can_manage_bots":true}}`)
	}))
	t.Cleanup(api.Close)
	s := bot.New(q, conn, new(sync.Mutex), nil, api.URL, nil)
	if err := s.ConfigureTelegramVault(ctx, store); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTelegramManager(ctx, "456:old"); err != nil {
		t.Fatal(err)
	}
	oldRef, err := q.TelegramManagerCredential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// When SQLite refuses the replacement, its test error identifies the unused ref.
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_manager_replacement BEFORE INSERT ON telegram_manager
		BEGIN SELECT RAISE(ABORT, NEW.credential_ref); END`); err != nil {
		t.Fatal(err)
	}
	saveErr := s.SetTelegramManager(ctx, "456:new")
	if saveErr == nil {
		t.Fatal("expected the replacement to fail")
	}
	const prefix = "telegram-manager-"
	_, suffix, ok := strings.Cut(saveErr.Error(), prefix)
	if !ok || len(suffix) < len(uuid.UUID{}.String()) {
		t.Fatalf("replacement failed before it reached the writer: %v", saveErr)
	}
	ref := prefix + suffix[:len(uuid.UUID{}.String())]

	// Then only the previous manager credential remains available.
	savedRef, dbErr := q.TelegramManagerCredential(ctx)
	old, oldErr := store.Credential(oldRef)
	unused, unusedErr := store.Credential(ref)
	if dbErr != nil || oldErr != nil || unusedErr != nil || savedRef != oldRef || old != "456:old" || unused != "" {
		t.Fatalf("after failed save: old ref kept=%t old credential kept=%t unused credential kept=%t, read errs: %v, %v, %v", savedRef == oldRef, old == "456:old", unused != "", dbErr, oldErr, unusedErr)
	}
}
