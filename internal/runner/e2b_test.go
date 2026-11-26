package runner_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/runner/runnertest"
)

func TestUserE2BManagerPersistsResumesRecoversAndResetsSandbox(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFakeE2B(t, "e2b_test_secret")
	dataDir := t.TempDir()

	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	now := time.Now().UnixMilli()
	envRow, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-e2b", Name: "E2B Cloud", Kind: string(runner.KindE2B),
		Config: `{"template":"base","timeoutSeconds":120}`, Secret: "e2b_test_secret",
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateBot(t.Context(), sqlc.CreateBotParams{
		ID: "bot-e2b", Name: "E2B Bot", ThinkingLevel: "off", RunnerID: envRow.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	newMgr := func() *runner.Manager {
		return runner.NewManager(runner.ManagerConfig{
			DataDir:    dataDir,
			Queries:    q,
			E2BAPIURL:  fake.APIURL(),
			E2BEnvdURL: fake.EnvdURL(),
		})
	}

	mgr := newMgr()
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// Probe creates and cleans up a temporary probe sandbox.
	probe, err := mgr.Probe(t.Context(), new(runner.SpecFromRow(envRow)), runner.SpecFromRow(envRow))
	if err != nil || !probe.OK || probe.Home != "/home/user" {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	if got := fake.Counts(); got.Created != 1 || got.Killed != 1 {
		t.Fatalf("probe counts=%+v want created=1 killed=1", got)
	}

	// First Open creates persistent sandbox sbx-2 and stores sandbox_id on bots.
	h, err := mgr.Open(t.Context(), "bot-e2b", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.LastCreateRunnerID(); got != envRow.ID {
		t.Fatalf("sandbox Runner ID = %q, want %q", got, envRow.ID)
	}
	env, err := h.Env(t.Context(), "", map[string]string{"EXPLICIT_E2B": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.WriteFile(t.Context(), "notes/todo.txt", "line1\nline2\n"); err != nil {
		t.Fatal(err)
	}
	reader, err := env.OpenBinaryReader(t.Context(), "notes/todo.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := reader.ScanLines(t.Context(), durableenv.ScanLinesOptions{StartLine: 2})
	_ = reader.Close(t.Context())
	if err != nil || scan.Newlines != 2 {
		t.Fatalf("scan=%+v err=%v", scan, err)
	}

	var stdout strings.Builder
	res, err := env.Exec(t.Context(), `printf "%s|%s" "$REMOTE_E2B_MARKER" "$EXPLICIT_E2B"`, &durableenv.ShellExecOptions{
		InheritEnv: new(true),
		OnOutput:   func(_ context.Context, text string, _ durableenv.ShellOutputInfo) { stdout.WriteString(text) },
	})
	if err != nil || res.ExitCode != 0 || stdout.String() != "e2b-fake-ok|ok" {
		t.Fatalf("exec=%+v err=%v out=%q", res, err, stdout.String())
	}
	_ = h.Close(t.Context())

	botRow, err := q.GetBot(t.Context(), "bot-e2b")
	if err != nil || botRow.RunnerSandboxID != "sbx-2" {
		t.Fatalf("persisted sandbox=%q err=%v", botRow.RunnerSandboxID, err)
	}

	// Simulate E2B idle autoPause + dex restart (fresh Manager): next Open reconnects via /connect and preserves files.
	fake.SimulateAutoPause("sbx-2")
	mgr2 := newMgr()
	t.Cleanup(func() { _ = mgr2.Close(context.Background()) })

	h2, err := mgr2.Open(t.Context(), "bot-e2b", "")
	if err != nil {
		t.Fatal(err)
	}
	env2, err := h2.Env(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	text, err := env2.ReadTextFile(t.Context(), "notes/todo.txt")
	if err != nil || text != "line1\nline2\n" {
		t.Fatalf("read after resume=%q err=%v", text, err)
	}
	if got := fake.Counts(); got.Created != 2 || got.Connected < 1 {
		t.Fatalf("expected reconnect without new sandbox, counts=%+v", got)
	}

	// ResetBot kills sbx-2 and clears sandbox_id; next Open provisions sbx-3.
	if err := mgr2.ResetBot(t.Context(), "bot-e2b"); err != nil {
		t.Fatal(err)
	}
	botAfterReset, _ := q.GetBot(t.Context(), "bot-e2b")
	if botAfterReset.RunnerSandboxID != "" {
		t.Fatalf("expected cleared sandbox_id, got %q", botAfterReset.RunnerSandboxID)
	}

	h3, err := mgr2.Open(t.Context(), "bot-e2b", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = h3.Close(t.Context())
	botFresh, _ := q.GetBot(t.Context(), "bot-e2b")
	if botFresh.RunnerSandboxID != "sbx-3" {
		t.Fatalf("expected fresh sbx-3 after reset, got %q", botFresh.RunnerSandboxID)
	}

	// Simulate expired sandbox (404 on /connect): fresh Manager transparently creates sbx-4.
	fake.SimulateExpire("sbx-3")
	mgr3 := newMgr()
	t.Cleanup(func() { _ = mgr3.Close(context.Background()) })
	h4, err := mgr3.Open(t.Context(), "bot-e2b", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = h4.Close(t.Context())
	botRecovered, _ := q.GetBot(t.Context(), "bot-e2b")
	if botRecovered.RunnerSandboxID != "sbx-4" {
		t.Fatalf("expected transparent 404 recovery sbx-4, got %q", botRecovered.RunnerSandboxID)
	}

	// Releasing the earlier sandbox keeps the reference to the sandbox that a later open stored.
	if err := mgr3.ReleaseBot(t.Context(), "bot-e2b", envRow.ID, "sbx-3"); err != nil {
		t.Fatal(err)
	}
	if botKept, _ := q.GetBot(t.Context(), "bot-e2b"); botKept.RunnerSandboxID != "sbx-4" {
		t.Fatalf("release of sbx-3 cleared the later sandbox reference, got %q", botKept.RunnerSandboxID)
	}
}

func TestUserConnectsCustomE2BEndpointsWithoutSecretAndWithoutAutoPause(t *testing.T) {
	t.Parallel()
	// Custom local/self-hosted E2B runtime with no API key and no /connect pause-resume support.
	fake := runnertest.NewFakeE2B(t, "")
	fake.SetConnectUnsupported(true)

	// Path-prefix proxy simulating OpenKruise-style /{sandboxId}/{port} envd routing.
	var (
		gwMu             sync.Mutex
		seenTemplatePath string
	)
	gwSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/proxy/")
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		gwMu.Lock()
		seenTemplatePath = "/" + parts[0] + "/" + parts[1]
		gwMu.Unlock()
		targetURL := fake.EnvdURL() + "/" + parts[2]
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}
		proxyReq, reqErr := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
		if reqErr != nil {
			http.Error(w, reqErr.Error(), http.StatusBadRequest)
			return
		}
		proxyReq.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(gwSrv.Close)

	dataDir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	now := time.Now().UnixMilli()
	cfgJSON := fmt.Sprintf(
		`{"apiUrl":%q,"envdUrl":%q,"template":"base","timeoutSeconds":90,"autoPause":false}`,
		fake.APIURL(),
		gwSrv.URL+"/proxy/{sandboxId}/{port}",
	)
	envRow, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-custom-e2b", Name: "Local Docker E2B", Kind: string(runner.KindE2B),
		Config: cfgJSON, Secret: "", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateBot(t.Context(), sqlc.CreateBotParams{
		ID: "bot-custom-e2b", Name: "Custom E2B Bot", ThinkingLevel: "off", RunnerID: envRow.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Manager has NO global E2BAPIURL or E2BEnvdURL set: it must resolve endpoints from envRow.Config.
	newMgr := func() *runner.Manager {
		return runner.NewManager(runner.ManagerConfig{
			DataDir: dataDir,
			Queries: q,
		})
	}
	mgr := newMgr()
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	probe, err := mgr.Probe(t.Context(), new(runner.SpecFromRow(envRow)), runner.SpecFromRow(envRow))
	if err != nil || !probe.OK || probe.Home != "/home/user" {
		t.Fatalf("custom e2b probe=%+v err=%v", probe, err)
	}
	wantArch := runtime.GOARCH
	if wantArch == "amd64" {
		wantArch = "x64"
	}
	if probe.Platform != runtime.GOOS || probe.Arch != wantArch {
		t.Fatalf("unexpected detected platform/arch: got %s/%s want %s/%s", probe.Platform, probe.Arch, runtime.GOOS, wantArch)
	}
	gwMu.Lock()
	gotTemplatePath := seenTemplatePath
	gwMu.Unlock()
	if gotTemplatePath != "/sbx-1/49983" {
		t.Fatalf("expected template path /sbx-1/49983, got %q", gotTemplatePath)
	}
	if ap, ar := fake.LastCreateAutoPause(); ap || ar {
		t.Fatalf("expected autoPause/autoResume omitted when autoPause=false, got ap=%v ar=%v", ap, ar)
	}

	h, err := mgr.Open(t.Context(), "bot-custom-e2b", "")
	if err != nil {
		t.Fatal(err)
	}
	env, err := h.Env(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.WriteFile(t.Context(), "marker.txt", "custom-ok"); err != nil {
		t.Fatal(err)
	}
	_ = h.Close(t.Context())

	// Fresh manager reconnects when /connect returns 501 by falling back to /timeout without creating a new sandbox.
	mgr2 := newMgr()
	t.Cleanup(func() { _ = mgr2.Close(context.Background()) })
	createdBefore := fake.Counts().Created
	renewedBefore := fake.Counts().Renewed
	h2, err := mgr2.Open(t.Context(), "bot-custom-e2b", "")
	if err != nil {
		t.Fatal(err)
	}
	env2, err := h2.Env(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotText, err := env2.ReadTextFile(t.Context(), "marker.txt")
	if err != nil || gotText != "custom-ok" {
		t.Fatalf("read after reconnect=%q err=%v", gotText, err)
	}
	if fake.Counts().Created != createdBefore || fake.Counts().Renewed <= renewedBefore {
		t.Fatalf("expected /timeout fallback without new sandbox, counts=%+v", fake.Counts())
	}
	_ = h2.Close(t.Context())

	// ResetBot and ReleaseBot must still DELETE the sandbox even when Secret is empty.
	killsBeforeReset := fake.Counts().Killed
	if err := mgr2.ResetBot(t.Context(), "bot-custom-e2b"); err != nil {
		t.Fatal(err)
	}
	if fake.Counts().Killed <= killsBeforeReset {
		t.Fatalf("expected ResetBot to delete custom keyless sandbox, counts=%+v", fake.Counts())
	}

	h3, err := mgr2.Open(t.Context(), "bot-custom-e2b", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = h3.Close(t.Context())
	botRow, _ := q.GetBot(t.Context(), "bot-custom-e2b")
	killsBeforeCleanup := fake.Counts().Killed
	if err := mgr2.ReleaseBot(t.Context(), "bot-custom-e2b", envRow.ID, botRow.RunnerSandboxID); err != nil {
		t.Fatal(err)
	}
	if fake.Counts().Killed <= killsBeforeCleanup {
		t.Fatalf("expected ReleaseBot to delete custom keyless sandbox, counts=%+v", fake.Counts())
	}
}

func TestUserConnectsLocalhostSubdomainE2BViaLoopbackDialer(t *testing.T) {
	t.Parallel()
	fake := runnertest.NewFakeE2B(t, "")
	baseURL, lastEnvdHost := fake.StartCombinedServer(t)
	localhostAPI := strings.Replace(baseURL, "http://127.0.0.1:", "http://localhost:", 1)

	mgr := runner.NewManager(runner.ManagerConfig{DataDir: t.TempDir()})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	probe, err := mgr.Probe(t.Context(), nil, runner.Spec{
		ID:     "env-localhost",
		Kind:   runner.KindE2B,
		Config: fmt.Sprintf(`{"apiUrl":%q}`, localhostAPI),
	})
	if err != nil || !probe.OK {
		t.Fatalf("localhost subdomain probe=%+v err=%v", probe, err)
	}
	if !strings.HasPrefix(lastEnvdHost(), "49983-sbx-1.localhost:") {
		t.Fatalf("expected Host header 49983-sbx-1.localhost:<port>, got %q", lastEnvdHost())
	}
}
