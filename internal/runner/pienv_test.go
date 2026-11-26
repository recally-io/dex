package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/runner/runnertest"
)

func TestMain(m *testing.M) {
	runnertest.DispatchHelperMain()
	os.Exit(m.Run())
}

func TestUserPiEnvManagerConnectsWithTOFUAndRejectsChangedHostKey(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sshFix := runnertest.NewFakeSSH(t)

	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	now := time.Now().UnixMilli()
	envRow, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-ssh", Name: "Dev SSH", Kind: string(runner.KindPiEnv),
		Config: `{"host":"devbox.internal","workspaceDir":"~/workspaces"}`, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateAgent(t.Context(), sqlc.CreateAgentParams{
		ID: "bot-ssh", Name: "SSH Bot", ThinkingLevel: "off", RunnerID: envRow.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	mgr := runner.NewManager(runner.ManagerConfig{
		DataDir:    dataDir,
		Queries:    q,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// First TestRunner trusts host key via TOFU and succeeds.
	probe, err := mgr.Probe(t.Context(), new(runner.SpecFromRow(envRow)), runner.SpecFromRow(envRow))
	if err != nil || !probe.OK || probe.Home != sshFix.RemoteHome {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}

	// Open creates per-agent workspace and runs remote commands over PiG/env.
	h, err := mgr.Open(t.Context(), "bot-ssh", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close(context.Background()) }()

	wantWS := filepath.Join(sshFix.RemoteHome, "workspaces", "bot-ssh", "workspace")
	if h.Workspace() != wantWS {
		t.Fatalf("workspace=%q want=%q", h.Workspace(), wantWS)
	}
	env, err := h.Env(t.Context(), "", map[string]string{"EXPLICIT_PI": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := env.Exec(t.Context(), `printf "%s|%s" "$REMOTE_SSH_MARKER" "$EXPLICIT_PI"`, &durableenv.ShellExecOptions{
		InheritEnv: new(true),
		OnOutput:   func(_ context.Context, text string, _ durableenv.ShellOutputInfo) { out.WriteString(text) },
	})
	if err != nil || res.ExitCode != 0 || out.String() != "ssh-fake-ok|ok" {
		t.Fatalf("exec res=%+v err=%v out=%q", res, err, out.String())
	}

	// Rotating the remote host key fails verification with HostKeyChanged=true until forgotten (e.g. on host change).
	sshFix.RotateHostKey(t)
	probeChanged, err := mgr.Probe(t.Context(), new(runner.SpecFromRow(envRow)), runner.SpecFromRow(envRow))
	if err != nil || probeChanged.OK || !probeChanged.HostKeyChanged {
		t.Fatalf("expected changed host key probe failure with HostKeyChanged=true, got %+v err=%v", probeChanged, err)
	}
	if err := mgr.ForgetHostKey(runner.SpecFromRow(envRow)); err != nil {
		t.Fatal(err)
	}
	probeReset, err := mgr.Probe(t.Context(), new(runner.SpecFromRow(envRow)), runner.SpecFromRow(envRow))
	if err != nil || !probeReset.OK || probeReset.HostKeyChanged {
		t.Fatalf("expected probe recovery after forget, got %+v err=%v", probeReset, err)
	}
}

func TestUserProbesUnsavedPiEnvWithoutPollutingKnownHosts(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sshFix := runnertest.NewFakeSSH(t)

	mgr := runner.NewManager(runner.ManagerConfig{
		DataDir:    dataDir,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// When probing an unsaved SSH environment (saved == nil), connectivity succeeds
	// without writing into <dataDir>/ssh_known_hosts.
	probe, err := mgr.Probe(t.Context(), nil, runner.Spec{
		Kind:   runner.KindPiEnv,
		Config: `{"host":"preview.internal","workspaceDir":"~/work"}`,
	})
	if err != nil || !probe.OK || probe.Home != sshFix.RemoteHome {
		t.Fatalf("ephemeral probe=%+v err=%v", probe, err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "ssh_known_hosts")); !os.IsNotExist(statErr) {
		t.Fatalf("expected <dataDir>/ssh_known_hosts to stay untouched, statErr=%v", statErr)
	}
}

func TestUserPiEnvUsesLoginShellByDefaultAndRespectsOptOut(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sshFix := runnertest.NewFakeSSH(t)
	profile := "echo 'welcome banner'\nexport REMOTE_LOGIN_VAR=from-login\ncase \"$-\" in\n  *i*) export REMOTE_INTERACTIVE_VAR=from-interactive ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(sshFix.RemoteHome, ".profile"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	now := time.Now().UnixMilli()
	if _, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-login-default", Name: "Default Login", Kind: string(runner.KindPiEnv),
		Config: `{"host":"login.internal"}`, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-login-off", Name: "No Login", Kind: string(runner.KindPiEnv),
		Config: `{"host":"nologin.internal","loginShell":false}`, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []sqlc.CreateAgentParams{
		{ID: "bot-login", Name: "Login Bot", ThinkingLevel: "off", RunnerID: "env-login-default", CreatedAt: now},
		{ID: "bot-nologin", Name: "No Login Bot", ThinkingLevel: "off", RunnerID: "env-login-off", CreatedAt: now},
	} {
		if _, err := q.CreateAgent(t.Context(), agent); err != nil {
			t.Fatal(err)
		}
	}

	mgr := runner.NewManager(runner.ManagerConfig{
		DataDir:    dataDir,
		Queries:    q,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// Default loginShell (omitted -> true) captures login & interactive shell exports.
	hLogin, err := mgr.Open(t.Context(), "bot-login", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hLogin.Close(context.Background()) }()
	envLogin, err := hLogin.Env(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var outLogin strings.Builder
	res, err := envLogin.Exec(t.Context(), `printf "%s|%s" "${REMOTE_LOGIN_VAR:-}" "${REMOTE_INTERACTIVE_VAR:-}"`, &durableenv.ShellExecOptions{
		InheritEnv: new(true),
		OnOutput:   func(_ context.Context, text string, _ durableenv.ShellOutputInfo) { outLogin.WriteString(text) },
	})
	if err != nil || res.ExitCode != 0 || outLogin.String() != "from-login|from-interactive" {
		t.Fatalf("default loginShell exec res=%+v err=%v out=%q", res, err, outLogin.String())
	}

	// Explicit loginShell=false skips login and interactive rc startup.
	hNoLogin, err := mgr.Open(t.Context(), "bot-nologin", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hNoLogin.Close(context.Background()) }()
	envNoLogin, err := hNoLogin.Env(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var outNoLogin strings.Builder
	res, err = envNoLogin.Exec(t.Context(), `printf "%s|%s|%s" "$REMOTE_SSH_MARKER" "${REMOTE_LOGIN_VAR:-}" "${REMOTE_INTERACTIVE_VAR:-}"`, &durableenv.ShellExecOptions{
		InheritEnv: new(true),
		OnOutput:   func(_ context.Context, text string, _ durableenv.ShellOutputInfo) { outNoLogin.WriteString(text) },
	})
	if err != nil || res.ExitCode != 0 || outNoLogin.String() != "ssh-fake-ok||" {
		t.Fatalf("loginShell=false exec res=%+v err=%v out=%q", res, err, outNoLogin.String())
	}
}

func TestUserPiEnvBotsShareOneConnectionUntilConfigChanges(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sshFix := runnertest.NewFakeSSH(t)
	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	// Given two bots on one SSH environment.
	now := time.Now().UnixMilli()
	envRow, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-shared", Name: "Shared SSH", Kind: string(runner.KindPiEnv),
		Config: `{"host":"shared.internal","workspaceDir":"~/one"}`, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bot-a", "bot-b"} {
		if _, err := q.CreateAgent(t.Context(), sqlc.CreateAgentParams{ID: id, Name: id, ThinkingLevel: "off", RunnerID: envRow.ID, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	mgr := runner.NewManager(runner.ManagerConfig{DataDir: dataDir, Queries: q, SSHProgram: sshFix.SSHProgram})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	run := func(h runner.Handle) {
		t.Helper()
		env, err := h.Env(t.Context(), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := env.Exec(t.Context(), "true", nil); err != nil || res.ExitCode != 0 {
			t.Fatalf("exec res=%+v err=%v", res, err)
		}
	}

	// When both bots open the environment.
	hA, err := mgr.Open(t.Context(), "bot-a", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hA.Close(context.Background()) }()
	run(hA)
	afterFirst := sshFix.Invocations(t)
	hB, err := mgr.Open(t.Context(), "bot-b", "")
	if err != nil {
		t.Fatal(err)
	}
	run(hB)
	_ = hB.Close(context.Background())

	// Then the second bot reuses the SSH connection, and each handle carries the current stamp.
	if got := sshFix.Invocations(t); got != afterFirst {
		t.Fatalf("second bot ran ssh %d more times, want 0", got-afterFirst)
	}
	if stamp, err := mgr.Stamp(t.Context(), "bot-a", ""); err != nil || stamp != hA.Stamp() || hA.Stamp() == "" {
		t.Fatalf("stamp=%q err=%v handle=%q", stamp, err, hA.Stamp())
	}

	// When the environment changes.
	updated, err := q.UpdateRunner(t.Context(), sqlc.UpdateRunnerParams{
		Name: envRow.Name, Kind: envRow.Kind, Config: `{"host":"shared.internal","workspaceDir":"~/two"}`, UpdatedAt: now + 1, ID: envRow.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Changed(t.Context(), runner.SpecFromRow(envRow), new(runner.SpecFromRow(updated))); err != nil {
		t.Fatal(err)
	}

	// Then the open lease still works but is stale, and the next open connects with the new configuration.
	run(hA)
	if stamp, _ := mgr.Stamp(t.Context(), "bot-a", ""); stamp == hA.Stamp() {
		t.Fatal("stamp did not change after the environment changed")
	}
	beforeReopen := sshFix.Invocations(t)
	hA2, err := mgr.Open(t.Context(), "bot-a", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hA2.Close(context.Background()) }()
	run(hA2)
	if sshFix.Invocations(t) == beforeReopen {
		t.Fatal("open after a configuration change reused the retired connection")
	}
	if want := filepath.Join(sshFix.RemoteHome, "two", "bot-a", "workspace"); hA2.Workspace() != want {
		t.Fatalf("workspace=%q want=%q", hA2.Workspace(), want)
	}

	// When the user forgets the host key, the loaded bot is stale and the next open makes a new handshake.
	if err := mgr.ForgetHostKey(runner.SpecFromRow(updated)); err != nil {
		t.Fatal(err)
	}
	if stamp, _ := mgr.Stamp(t.Context(), "bot-a", ""); stamp == hA2.Stamp() {
		t.Fatal("stamp did not change after the host key was forgotten")
	}
	beforeForgetOpen := sshFix.Invocations(t)
	hA3, err := mgr.Open(t.Context(), "bot-a", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hA3.Close(context.Background()) }()
	if sshFix.Invocations(t) == beforeForgetOpen {
		t.Fatal("open after forgetting the host key reused the trusted connection")
	}
}

func TestUserPiEnvOpenFailsWhenDexStopsDuringTheFirstConnect(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sshFix := runnertest.NewFakeSSH(t)
	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	now := time.Now().UnixMilli()
	if _, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-slow", Name: "Slow SSH", Kind: string(runner.KindPiEnv), Config: `{"host":"slow.internal"}`, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateAgent(t.Context(), sqlc.CreateAgentParams{ID: "bot-slow", Name: "Slow", ThinkingLevel: "off", RunnerID: "env-slow", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mgr := runner.NewManager(runner.ManagerConfig{DataDir: dataDir, Queries: q, SSHProgram: sshFix.SSHProgram})

	// Given a first open that waits inside its SSH connect.
	sshFix.Hold(t)
	t.Cleanup(func() { sshFix.Resume(t) })
	type opened struct {
		h   runner.Handle
		err error
	}
	result := make(chan opened, 1)
	go func() {
		h, err := mgr.Open(t.Context(), "bot-slow", "")
		result <- opened{h, err}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for sshFix.Invocations(t) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the open did not start ssh")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// When dex stops and the connect then completes.
	if err := mgr.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	sshFix.Resume(t)

	// Then the open fails instead of returning a lease on a closed manager.
	select {
	case got := <-result:
		if got.err == nil {
			_ = got.h.Close(context.Background())
			t.Fatal("open returned a lease after the manager closed")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the open did not return after the connect completed")
	}
}

func TestUserPiEnvBuildsDaemonOutsideModuleCheckout(t *testing.T) {
	// Given a standalone dex process running in a directory without go.mod (like launchd/systemd WorkingDirectory=$HOME),
	// a relative DataDir (like DEX_DATA_DIR=.local/dev-data), and no network access to sum.golang.org.
	t.Chdir(t.TempDir())
	t.Setenv("GOPROXY", "off")
	dataDir := filepath.Join(".local", "dev-data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sshFix := runnertest.NewFakeSSH(t)

	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	now := time.Now().UnixMilli()
	envRow, err := q.CreateRunner(t.Context(), sqlc.CreateRunnerParams{
		ID: "env-standalone", Name: "MacMini", Kind: string(runner.KindPiEnv),
		Config: `{"host":"macmini.local","user":"vaayne","workspaceDir":"~/.dex/agents"}`, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	mgr := runner.NewManager(runner.ManagerConfig{
		DataDir:    dataDir,
		Queries:    q,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// When the user tests the pi-env SSH connection without any pre-built daemon cache.
	probe, err := mgr.Probe(t.Context(), new(runner.SpecFromRow(envRow)), runner.SpecFromRow(envRow))
	// Then dex builds and deploys the real pi-env daemon binary and reports OK.
	if err != nil || !probe.OK || probe.Home != sshFix.RemoteHome {
		t.Fatalf("standalone probe=%+v err=%v", probe, err)
	}
}
