package app_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUserExplicitTelegramTTSQueuesNativeVoiceWithoutChangingProvider(t *testing.T) {
	t.Parallel()
	// Given explicit consent, a selected provider/model/voice and a synthetic local audio response.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	var calls atomic.Int64
	audio := "OggS\x00\x00OpusHeadsynthetic spoken words"
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.MarshalWrite(w, map[string]any{"data": []any{}})
			return
		}
		if r.URL.Path != "/v1/audio/speech" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var request struct {
			Model  string `json:"model"`
			Text   string `json:"input"`
			Voice  string `json:"voice"`
			Format string `json:"response_format"`
		}
		if err := json.UnmarshalRead(r.Body, &request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.Model != "tts-explicit" || request.Text != "synthetic words" || request.Voice != "explicit-voice" || request.Format != "opus" {
			http.Error(w, "wrong explicit speech settings", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(audio))
	}))
	t.Cleanup(boundary.Close)
	configureTelegramSpeech(t, d, boundary.URL, nil, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "tts-explicit", "voice": "explicit-voice", "format": "opus"})
	d.setModel(t, "tool")
	f.message(1, 42, 42, `telegram_message {"action":"speak","text":"synthetic words"}`)
	// Then the native speech tool queues the exact provider bytes through the durable file sender.
	got := nextTelegramMedia(t, f.mediaSent)
	if got.Method != "sendVoice" || got.Body != audio || got.Chat != "42" {
		t.Fatalf("TTS delivery=%+v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("TTS calls=%d", calls.Load())
	}
	conn := controlWriteDB(t, data)
	eventually(t, "TTS result transferred and native delivery confirmed", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM telegram_effects WHERE kind='tts' AND state='done' AND body IS NULL")
		done := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL")
		return n == 1 && done == 1, []int{n, done}
	})
	d.stop()
	startDex(t, data, f.srv.URL)
	if calls.Load() != 1 {
		t.Fatalf("successful TTS repeated after restore: %d", calls.Load())
	}
}

func TestUserTelegramTTSIsOffByDefault(t *testing.T) {
	t.Parallel()
	// Given no speech configuration, the native tool must fail without a provider request.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "tool")
	f.message(1, 42, 42, `telegram_message {"action":"speak","text":"do not send this"}`)
	if got := f.next(t).Text; !strings.Contains(got, "TTS is off") {
		t.Fatalf("unconfigured speech=%q", got)
	}
	select {
	case audio := <-f.mediaAttempts:
		t.Fatalf("unconfigured speech sent audio: %+v", audio)
	default:
	}
}

func TestUserTelegramSpeechBudgetRefusesOversizeOrLongAudioBeforeProviderUpload(t *testing.T) {
	t.Parallel()
	// Given a duration budget shorter than the incoming synthetic voice message.
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	d := startTelegram(t, f, t.TempDir(), 42)
	boundary, calls := localSTT(t, "must not happen")
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit", "maxDurationSeconds": 2}, nil)
	f.voice()
	if got := f.next(t).Text; !strings.Contains(got, "duration budget") || !strings.Contains(got, "voice.ogg") {
		t.Fatalf("duration refusal=%q", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("over-budget audio sent to provider: %d", calls.Load())
	}
}

func TestUserInvalidTelegramSpeechOutputKeepsReadableText(t *testing.T) {
	t.Parallel()
	// Given an explicitly configured provider that returns an HTML page instead of audio.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	var calls atomic.Int64
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.MarshalWrite(w, map[string]any{"data": []any{}})
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte("<html>not audio</html>"))
	}))
	t.Cleanup(boundary.Close)
	configureTelegramSpeech(t, d, boundary.URL, nil, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "tts-explicit", "voice": "explicit-voice"})
	d.setModel(t, "tool")
	f.message(1, 42, 42, `telegram_message {"action":"speak","text":"keep these readable words"}`)
	// Then the native result retains the words and explains the failure, without sending a fake audio file.
	if got := f.next(t).Text; !strings.Contains(got, "keep these readable words") || !strings.Contains(got, "did not return audio") {
		t.Fatalf("speech fallback=%q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid speech retried: %d", calls.Load())
	}
	select {
	case audio := <-f.mediaAttempts:
		t.Fatalf("invalid speech became a file: %+v", audio)
	default:
	}
}
