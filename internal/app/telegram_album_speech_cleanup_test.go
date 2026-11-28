package app_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestUserRevokedSpeechStageKeepsAudioOwnedByAlbumInput(t *testing.T) {
	t.Parallel()
	// Given an audio album whose speech reference cannot clear after the handoff.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic audio"
	d := startTelegram(t, f, data, 42)
	provider, calls := localSTT(t, "recognised album audio")
	configureTelegramSpeech(t, d, provider.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit"})
	conn := controlWriteDB(t, data)
	execSQL(t, conn, `CREATE TRIGGER hold_album_speech_reference BEFORE UPDATE OF input_json ON telegram_effects
WHEN OLD.operation_key='stt:telegram:123:1' AND OLD.state='done' AND NEW.input_json='{}'
BEGIN SELECT RAISE(ABORT,'hold speech reference'); END`)
	update := telegramAlbumUpdate("audio-album", 1, "voice.ogg")
	message := update["message"].(map[string]any)
	delete(message, "document")
	message["audio"] = map[string]any{"file_id": "voice.ogg", "file_name": "voice.ogg", "duration": 3}
	f.mu.Lock()
	f.updates = append(f.updates, update)
	f.mu.Unlock()
	if reply := f.next(t); !strings.Contains(reply.Text, "recognised album audio") {
		t.Fatalf("audio album reply=%q", reply.Text)
	}
	eventually(t, "album receipt owns the file and the speech stage still references it", func() (bool, any) {
		members := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='transferred'")
		effects := count(t, conn, "SELECT count(*) FROM telegram_effects WHERE state='done' AND input_json<>'{}'")
		return members == 1 && effects == 1, []int{members, effects}
	})
	var source string
	if err := conn.QueryRowContext(t.Context(), "SELECT batch_source FROM telegram_album_members WHERE source_key='telegram:123:1'").Scan(&source); err != nil {
		t.Fatal(err)
	}

	// When the owner disconnects Telegram, cleanup clears the stale reference and leaves the file in history.
	d.must(t, http.MethodDelete, "/api/bots/default/channels/telegram", nil, nil)
	if n := count(t, conn, "SELECT count(*) FROM telegram_effects WHERE state='discarded' AND input_json='{}'"); n != 1 {
		t.Fatalf("cleaned speech stage count=%d; want one", n)
	}
	assertReceiptUpload(t, conn, data, source)
	if n := calls.Load(); n != 1 {
		t.Fatalf("provider calls=%d; want one", n)
	}
}
