package outbound_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/runner/outbound"
)

func TestUserRunsAnotherBotsToolDuringAnOutboundProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	data := t.TempDir()
	dbConn, err := db.Open(ctx, filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbConn.Close()
	q := sqlc.New(dbConn)
	seed := func(query string) {
		t.Helper()
		if _, err := dbConn.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('alpha','Alpha','faux','echo',1),('beta','Beta','faux','echo',1)`)
	seed(`INSERT INTO conversations (id,agent_id,channel_key,created_at) VALUES (1,'alpha','main',1),(2,'beta','main',1)`)
	if _, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: "input", ConversationID: 1, Origin: "web", Producer: "user", Kind: "message", Content: "start", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	seed(`INSERT INTO execution_environments (id,name,kind,config,created_at,updated_at) VALUES ('remote','Remote','runner','{}',1,1)`)
	manager := runner.NewManager(runner.ManagerConfig{DataDir: data, Queries: q})
	defer manager.Close(context.WithoutCancel(ctx))
	machines := manager.Runners()
	code, err := machines.IssueCode(ctx, "remote")
	if err != nil {
		t.Fatal(err)
	}
	token, err := machines.Enroll(ctx, "remote", code)
	if err != nil {
		t.Fatal(err)
	}
	online := make(chan struct{}, 1)
	machines.OnOnline(func(string) { online <- struct{}{} })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{outbound.Version}})
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = socket.CloseNow() }()
		_ = machines.Accept(ctx, "remote", token, socket)
	}))
	defer server.Close()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{Subprotocols: []string{outbound.Version}}) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = outbound.Serve(ctx, socket, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work}))
	}()
	defer func() { _ = socket.CloseNow(); <-served }()
	receive(ctx, t, online)

	// Given a Process on one Bot that cannot exit until another Bot writes a marker.
	bg := background.New(dbConn, time.Now, background.Limits{}, nil)
	defer bg.Close()
	bg.ConfigureEnvOpener(func(ctx context.Context, agent, machine, cwd string, values map[string]string) (env.ExecutionEnv, string, runner.Kind, func(), error) {
		h, err := manager.Open(ctx, agent, machine)
		if err != nil {
			return nil, "", "", nil, err
		}
		release := func() { _ = h.Close(context.WithoutCancel(ctx)) }
		e, err := h.Env(ctx, cwd, values)
		if err != nil {
			release()
			return nil, "", "", nil, err
		}
		return e, h.Workspace(), h.Kind(), release, nil
	})
	row, err := bg.Start(ctx, background.StartParams{CallKey: "alpha/process", AgentID: "alpha", ConversationID: 1, SourceRequestID: "input", Wake: background.WakeNone,
		Command: `touch started; while [ ! -e finish ]; do sleep .05; done`, Cwd: work, DefaultCwd: work, EnvironmentID: "remote", EnvKind: runner.KindRunner, Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	// When a different Bot uses the same Runner socket, its tool can finish first.
	tool, err := manager.Open(ctx, "beta", "remote")
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close(context.WithoutCancel(ctx))
	e, err := tool.Env(ctx, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	result, err := e.Exec(ctx, []string{"/bin/sh", "-c", `while [ ! -e started ]; do sleep .05; done; printf concurrent; touch finish`}, &env.ShellExecOptions{Cwd: work, OnOutput: func(_ context.Context, s string, _ env.ShellOutputInfo) { output.WriteString(s) }})
	if err != nil || result.ExitCode != 0 || output.String() != "concurrent" {
		t.Fatalf("tool did not run concurrently: %v, %q, %v", result, output.String(), err)
	}
	rows, timeout, err := bg.Wait(ctx, 1, []string{row.ID}, 3*time.Second)
	if err != nil || timeout || len(rows) != 1 || !rows[0].ExitStatus.Valid || rows[0].ExitStatus.Int64 != 0 {
		t.Fatalf("Process did not exit after the tool: %+v, %v, %v", rows, timeout, err)
	}
}
