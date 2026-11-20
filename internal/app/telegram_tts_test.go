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
	for _, tc := range []struct {
		name, provider, reason string
		calls                  int64
	}{
		{"missing_provider", "missing", "provider is not configured", 0},
		{"invalid_audio", "speech", "did not return audio", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Given a separate bot and chat for each speech failure, with no earlier replies.
			f := newFakeTelegram(t)
			d := startTelegram(t, f, t.TempDir(), 42)
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
			configureTelegramSpeech(t, d, boundary.URL, nil, map[string]any{"enabled": true, "allowSending": true, "provider": tc.provider, "model": "tts-explicit", "voice": "explicit-voice"})
			d.setModel(t, "tool")

			// When speech fails, the reply keeps the words and explains the failure.
			f.message(1, 42, 42, `telegram_message {"action":"speak","text":"keep these readable words"}`)
			if got := f.next(t).Text; !strings.Contains(got, "keep these readable words") || !strings.Contains(got, tc.reason) {
				t.Fatalf("speech fallback=%q", got)
			}

			// Then no invalid audio is sent, and the provider is not retried.
			if got := calls.Load(); got != tc.calls {
				t.Fatalf("speech provider calls=%d want=%d", got, tc.calls)
			}
			select {
			case audio := <-f.mediaAttempts:
				t.Fatalf("invalid speech became a file: %+v", audio)
			default:
			}
		})
	}
}
