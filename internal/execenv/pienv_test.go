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
	sshFix := execenvtest.NewFakeSSH(t, dataDir)

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
		Home:       t.TempDir(),
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

	// Rotating the remote host key fails verification until forgotten (e.g. on host change).
	sshFix.RotateHostKey(t)
	probeChanged, err := mgr.TestEnvironment(t.Context(), execenv.SpecFromRow(envRow))
	if err != nil || probeChanged.OK {
		t.Fatalf("expected changed host key probe failure, got %+v err=%v", probeChanged, err)
	}
	if err := execenv.ForgetPiEnvHostKey(dataDir, envRow.ID); err != nil {
		t.Fatal(err)
	}
	probeReset, err := mgr.TestEnvironment(t.Context(), execenv.SpecFromRow(envRow))
	if err != nil || !probeReset.OK {
		t.Fatalf("expected probe recovery after forget, got %+v err=%v", probeReset, err)
	}
}
