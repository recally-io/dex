package app_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

func configureTelegramSpeech(t *testing.T, d *dex, endpoint string, stt map[string]any) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/settings/providers/speech", map[string]any{"api": "openai-completions", "baseUrl": endpoint + "/v1", "apiKey": "synthetic-local-key", "models": []string{"chat-only"}}, nil)
	options := map[string]any{"stt": stt}
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": options}, nil)
}

func (f *fakeTelegram) voice() {
	const update = int64(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_id": update, "message": map[string]any{"message_id": update, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "voice": map[string]any{"file_id": "voice.ogg", "duration": 3}}})
}

func localSTT(t *testing.T, text string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := new(atomic.Int64)
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.MarshalWrite(w, map[string]any{"data": []any{map[string]any{"id": "chat-only"}}})
			return
		}
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-local-key" {
			http.Error(w, "wrong credential", http.StatusUnauthorized)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(file)
		_ = file.Close()
		if !strings.Contains(string(body), "OpusHead") || r.FormValue("model") != "transcribe-explicit" {
			http.Error(w, "wrong audio or model", http.StatusBadRequest)
			return
		}
		_ = json.MarshalWrite(w, map[string]string{"text": text})
	}))
	t.Cleanup(boundary.Close)
	return boundary, calls
}

func TestUserConfiguredTelegramTranscriptionSurvivesAdmissionFailureAndRestart(t *testing.T) {
	t.Parallel()
	// Given explicit local speech configuration and an audio upload whose native receipt cannot commit.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic audio"
	d := startTelegram(t, f, data, 42)
	boundary, calls := localSTT(t, "recognised synthetic words")
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit"})
	conn := cleanupDB(t, data, "telegram:123:1")
	f.voice()
	eventually(t, "one persisted transcription before admission", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM telegram_effects WHERE operation_key='stt:telegram:123:1' AND state='done'")
		return calls.Load() == 1 && n == 1, []int64{calls.Load(), int64(n)}
	})
	d.stop()
	execSQL(t, conn, "DROP TRIGGER reject_upload")
	// When the app restarts, provider selection, transcript and original upload restore without a second HTTP call.
	startDex(t, data, f.srv.URL)
	if got := f.next(t).Text; !strings.Contains(got, "recognised synthetic words") {
		t.Fatalf("transcript=%q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("transcription repeated %d times", calls.Load())
	}
	if n := uploadCount(t, data); n != 1 {
		t.Fatalf("re-downloaded voice: uploads=%d", n)
	}
	assertReceiptUpload(t, conn, data, "telegram:123:1")
}

func TestUserEmptyTelegramTranscriptionKeepsOriginalAudioAndDoesNotRetry(t *testing.T) {
	t.Parallel()
	// Given a configured provider that returns an empty transcript.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	d := startTelegram(t, f, data, 42)
	boundary, calls := localSTT(t, "   ")
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit"})
	f.voice()
	if got := f.next(t).Text; !strings.Contains(got, "empty transcript") || !strings.Contains(got, "voice.ogg") {
		t.Fatalf("empty transcript fallback=%q", got)
	}
	d.stop()
	startDex(t, data, f.srv.URL)
	if calls.Load() != 1 {
		t.Fatalf("empty transcript retried: %d", calls.Load())
	}
}

func TestUserUnknownTelegramTranscriptionIsNotSentToProviderAgain(t *testing.T) {
	t.Parallel()
	// Given a provider response whose local outcome write fails.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	d := startTelegram(t, f, data, 42)
	boundary, calls := localSTT(t, "provider accepted")
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit"})
	conn := controlWriteDB(t, data)
	execSQL(t, conn, "CREATE TRIGGER hold_speech_result BEFORE UPDATE OF state ON telegram_effects WHEN NEW.state='done' BEGIN SELECT RAISE(ABORT,'hold speech result'); END")
	f.voice()
	eventually(t, "provider accepted before failed receipt", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM telegram_effects WHERE state='processing'")
		return calls.Load() == 1 && n == 1, []int64{calls.Load(), int64(n)}
	})
	d.stop()
	execSQL(t, conn, "DROP TRIGGER hold_speech_result")
	// When restored, an unknown result is disclosed and the original file reaches native input without another upload to speech.
	startDex(t, data, f.srv.URL)
	if got := f.next(t).Text; !strings.Contains(got, "unknown") || !strings.Contains(got, "voice.ogg") {
		t.Fatalf("unknown transcription=%q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("unknown provider outcome retried: %d", calls.Load())
	}
}

func TestUserTelegramTranscriptionDoesNotUploadOverDurationBudget(t *testing.T) {
	t.Parallel()
	// Given a duration budget shorter than the incoming voice message.
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	d := startTelegram(t, f, t.TempDir(), 42)
	boundary, calls := localSTT(t, "must not happen")
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit", "maxDurationSeconds": 2})

	// When the user sends the voice message, dex keeps the original audio without an upload to the provider.
	f.voice()
	if got := f.next(t).Text; !strings.Contains(got, "duration budget") || !strings.Contains(got, "voice.ogg") {
		t.Fatalf("duration refusal=%q", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("over-budget audio sent to provider: %d", calls.Load())
	}
}

func TestUserTelegramTranscriptionTimeoutKeepsAudioWithoutProviderRetry(t *testing.T) {
	t.Parallel()
	// Given an explicitly selected local endpoint that does not finish before the configured timeout.
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	var calls atomic.Int64
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.MarshalWrite(w, map[string]any{"data": []any{}})
			return
		}
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(boundary.Close)
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit", "timeoutSeconds": 1})
	f.voice()
	if got := f.next(t).Text; !strings.Contains(got, "outcome unknown") || !strings.Contains(got, "voice.ogg") {
		t.Fatalf("timeout=%q", got)
	}
	d.stop()
	startDex(t, data, f.srv.URL)
	if calls.Load() != 1 {
		t.Fatalf("timed out speech retried %d times", calls.Load())
	}
}

func TestUserMissingConfiguredTelegramSpeechProviderDoesNotChooseAnother(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	d := startTelegram(t, f, t.TempDir(), 42)
	// Transcription requires consent to send audio.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": map[string]any{"stt": map[string]any{"enabled": true, "provider": "private", "model": "transcriber"}}}, nil, http.StatusBadRequest)
	// An explicit model name is not proof that a provider exists or supports audio.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": map[string]any{"stt": map[string]any{"enabled": true, "allowSending": true, "provider": "missing", "model": "explicit"}}}, nil)
	f.voice()
	if got := f.next(t).Text; !strings.Contains(got, "provider is not configured") || !strings.Contains(got, "voice.ogg") {
		t.Fatalf("missing provider=%q", got)
	}
}

func TestUserTelegramSpeechResolvesConfiguredOnePasswordReferenceInMemory(t *testing.T) {
	t.Parallel()
	// Given a synthetic CLI boundary and explicit 1Password tool authentication.
	home := t.TempDir()
	bin := t.TempDir()
	data := t.TempDir()
	script := `#!/bin/sh
[ "$1" = read ] && [ "$2" = --no-newline ] && [ "$3" = 'op://Synthetic/provider/api-key' ] || exit 2
[ "$OP_SERVICE_ACCOUNT_TOKEN" = synthetic-auth ] || exit 3
printf synthetic-local-key
`
	if err := os.WriteFile(filepath.Join(bin, "op"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	a, err := app.Start(t.Context(), app.Config{DataDir: data, Home: home, FakeModel: true, TelegramAPIURL: f.srv.URL, ToolEnv: map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME": home}})
	if err != nil {
		t.Fatal(err)
	}
	d := &dex{app: a, srv: httptest.NewServer(a.Handler())}
	t.Cleanup(d.stop)
	d.setModel(t, "echo")
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	d.must(t, http.MethodPut, "/api/settings/onepassword", map[string]any{"mode": "service-account", "serviceAccountToken": "synthetic-auth"}, nil)
	boundary, calls := localSTT(t, "reference credential works")
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit"})
	d.must(t, http.MethodPut, "/api/settings/providers/speech", map[string]any{"api": "openai-completions", "baseUrl": boundary.URL + "/v1", "apiKey": "op://Synthetic/provider/api-key", "models": []string{"chat-only"}}, nil)
	// When STT starts, only its selected endpoint receives the resolved in-memory key.
	f.voice()
	if got := f.next(t).Text; !strings.Contains(got, "reference credential works") {
		t.Fatalf("credential reference=%q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("credential transcription calls=%d", calls.Load())
	}
	conn := controlWriteDB(t, data)
	var stored string
	if err := conn.QueryRowContext(t.Context(), "SELECT api_key FROM providers WHERE id='speech'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "op://Synthetic/provider/api-key" {
		t.Fatal("provider reference was replaced with resolved key")
	}
}

func TestUserRevokedTelegramSpeechCannotAdmitItsStagedAudio(t *testing.T) {
	t.Parallel()
	// Given an authorized audio upload whose external request is still in progress.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.files["voice.ogg"] = "OggS\x00\x00OpusHeadsynthetic"
	d := startTelegram(t, f, data, 42)
	entered := make(chan struct{})
	var calls atomic.Int64
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.MarshalWrite(w, map[string]any{"data": []any{}})
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		close(entered)
		<-r.Context().Done()
	}))
	t.Cleanup(boundary.Close)
	configureTelegramSpeech(t, d, boundary.URL, map[string]any{"enabled": true, "allowSending": true, "provider": "speech", "model": "transcribe-explicit"})
	f.voice()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("speech request did not start")
	}
	// When authorization is revoked, the runtime cancels the request and releases only its unadmitted upload.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{}}, nil)
	conn := controlWriteDB(t, data)
	eventually(t, "revoked staging released without native input", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM telegram_effects WHERE kind='stt' AND state='discarded' AND input_json='{}'")
		uploads := uploadCount(t, data)
		inputs := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'")
		return n == 1 && uploads == 0 && inputs == 0, []int{n, uploads, inputs}
	})
	// Regranting the same actor does not revive the withdrawn update or send its audio again.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}}, nil)
	f.message(2, 42, 42, "new authorized message")
	if got := f.next(t).Text; !strings.Contains(got, "new authorized message") {
		t.Fatalf("revoked update revived: %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("revoked provider request repeated: %d", calls.Load())
	}
}
