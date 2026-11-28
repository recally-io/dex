package app

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/channel/telegram/policy"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

func TestUserStagedSourceDoesNotExposeAnotherBotsUpload(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	for _, bot := range []string{"owner", "other"} {
		if err := q.CreateBotIfMissing(ctx, sqlc.CreateBotIfMissingParams{ID: bot, Name: bot}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: "owner", Token: "123:fixture", AllowedUsers: "42"}); err != nil {
		t.Fatal(err)
	}
	// Given one received album member and one speech claim that has not been received.
	const album, speech = "telegram:123:1", "telegram:123:2"
	sessions := session.New(conn, nil, time.Now, nil, nil)
	bindSessionChannels(sessions)
	p := policy.New(conn, sessions, "owner")
	if err := p.StoreTelegramAlbum(ctx, "telegram:42", album, session.Message{
		Attachments:    []engine.Attachment{{Name: "album.txt"}},
		ChannelContext: session.ChannelContext{AccountID: "123", SenderID: "42", MediaGroup: "album"},
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := q.CreateTelegramEffect(ctx, sqlc.CreateTelegramEffectParams{
		OperationKey: "stt:" + speech, SourceKey: speech, BotID: "owner", AccountID: "123", SenderID: "42",
		Peer: "telegram:42", Kind: "stt", InputJson: `{"Attachments":[{"name":"voice.ogg"}]}`,
	}); err != nil || n != 1 {
		t.Fatalf("stage speech: inserted=%d err=%v", n, err)
	}
	// Then only the owning bot can see upload ownership, even though member deduplication is global.
	for source, wantReceived := range map[string]bool{album: true, speech: false} {
		if got, err := sessions.ReceivedSource(ctx, source); err != nil || got != wantReceived {
			t.Fatalf("%s received=%v err=%v; want %v", source, got, err, wantReceived)
		}
		if got, err := sessions.LookupSource(ctx, "owner", source); err != nil || !got.Staged || got.ID != "" {
			t.Fatalf("%s owner receipt=%+v err=%v", source, got, err)
		}
		if got, err := sessions.LookupSource(ctx, "other", source); !errors.Is(err, session.ErrSessionNotFound) || got != (session.SourceReceipt{}) {
			t.Fatalf("%s foreign receipt=%+v err=%v", source, got, err)
		}
	}
}
