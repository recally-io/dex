package execenv_test

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
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

func TestMain(m *testing.M) {
	execenvtest.DispatchHelperMain()
	os.Exit(m.Run())
}

func TestUserPiEnvManagerConnectsWithTOFUAndRejectsChangedHostKey(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sshFix := execenvtest.NewFakeSSH(t)

	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	now := time.Now().UnixMilli()
	envRow, err := q.CreateEnvironment(t.Context(), sqlc.CreateEnvironmentParams{
		ID: "env-ssh", Name: "Dev SSH", Kind: string(execenv.KindPiEnv),
		Config: `{"host":"devbox.internal","workspaceDir":"~/workspaces"}`, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateAgent(t.Context(), sqlc.CreateAgentParams{
		ID: "bot-ssh", Name: "SSH Bot", ThinkingLevel: "off", EnvironmentID: envRow.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	mgr := execenv.NewManager(execenv.ManagerConfig{
		DataDir:    dataDir,
		Queries:    q,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// First TestEnvironment trusts host key via TOFU and succeeds.
	probe, err := mgr.TestEnvironment(t.Context(), execenv.SpecFromRow(envRow))
	if err != nil || !probe.OK || probe.Home != sshFix.RemoteHome {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}

	// Open creates per-agent workspace and runs remote commands over PiG/env.
	h, err := mgr.Open(t.Context(), "bot-ssh")
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
	probeChanged, err := mgr.TestEnvironment(t.Context(), execenv.SpecFromRow(envRow))
	if err != nil || probeChanged.OK || !probeChanged.HostKeyChanged {
		t.Fatalf("expected changed host key probe failure with HostKeyChanged=true, got %+v err=%v", probeChanged, err)
	}
	if err := mgr.ForgetHostKey(execenv.SpecFromRow(envRow)); err != nil {
		t.Fatal(err)
	}
	probeReset, err := mgr.TestEnvironment(t.Context(), execenv.SpecFromRow(envRow))
	if err != nil || !probeReset.OK || probeReset.HostKeyChanged {
		t.Fatalf("expected probe recovery after forget, got %+v err=%v", probeReset, err)
	}
}

func TestUserProbesUnsavedPiEnvWithoutPollutingKnownHosts(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sshFix := execenvtest.NewFakeSSH(t)

	mgr := execenv.NewManager(execenv.ManagerConfig{
		DataDir:    dataDir,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// When probing an unsaved SSH environment (saved == nil), connectivity succeeds
	// without writing into <dataDir>/ssh_known_hosts.
	probe, err := mgr.ProbeEnvironment(t.Context(), nil, execenv.EnvironmentSpec{
		Kind:   execenv.KindPiEnv,
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
	sshFix := execenvtest.NewFakeSSH(t)
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
	if _, err := q.CreateEnvironment(t.Context(), sqlc.CreateEnvironmentParams{
		ID: "env-login-default", Name: "Default Login", Kind: string(execenv.KindPiEnv),
		Config: `{"host":"login.internal"}`, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateEnvironment(t.Context(), sqlc.CreateEnvironmentParams{
		ID: "env-login-off", Name: "No Login", Kind: string(execenv.KindPiEnv),
		Config: `{"host":"nologin.internal","loginShell":false}`, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []sqlc.CreateAgentParams{
		{ID: "bot-login", Name: "Login Bot", ThinkingLevel: "off", EnvironmentID: "env-login-default", CreatedAt: now},
		{ID: "bot-nologin", Name: "No Login Bot", ThinkingLevel: "off", EnvironmentID: "env-login-off", CreatedAt: now},
	} {
		if _, err := q.CreateAgent(t.Context(), agent); err != nil {
			t.Fatal(err)
		}
	}

	mgr := execenv.NewManager(execenv.ManagerConfig{
		DataDir:    dataDir,
		Queries:    q,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// Default loginShell (omitted -> true) captures login & interactive shell exports.
	hLogin, err := mgr.Open(t.Context(), "bot-login")
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
	hNoLogin, err := mgr.Open(t.Context(), "bot-nologin")
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

func TestUserPiEnvBuildsDaemonOutsideModuleCheckout(t *testing.T) {
	// Given a standalone dex process running in a directory without go.mod (like launchd/systemd WorkingDirectory=$HOME),
	// a relative DataDir (like DEX_DATA_DIR=.local/dev-data), and no network access to sum.golang.org.
	t.Chdir(t.TempDir())
	t.Setenv("GOPROXY", "off")
	dataDir := filepath.Join(".local", "dev-data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sshFix := execenvtest.NewFakeSSH(t)

	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)

	now := time.Now().UnixMilli()
	envRow, err := q.CreateEnvironment(t.Context(), sqlc.CreateEnvironmentParams{
		ID: "env-standalone", Name: "MacMini", Kind: string(execenv.KindPiEnv),
		Config: `{"host":"macmini.local","user":"vaayne","workspaceDir":"~/.dex/agents"}`, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	mgr := execenv.NewManager(execenv.ManagerConfig{
		DataDir:    dataDir,
		Queries:    q,
		SSHProgram: sshFix.SSHProgram,
	})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })

	// When the user tests the pi-env SSH connection without any pre-built daemon cache.
	probe, err := mgr.TestEnvironment(t.Context(), execenv.SpecFromRow(envRow))
	// Then dex builds and deploys the real pi-env daemon binary and reports OK.
	if err != nil || !probe.OK || probe.Home != sshFix.RemoteHome {
		t.Fatalf("standalone probe=%+v err=%v", probe, err)
	}
}
