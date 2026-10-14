// Package app wires control: the database, the worker process, the chat service, and the HTTP
// handler.
package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/dex/internal/chat"
	"github.com/vaayne/dex/internal/db"
	"github.com/vaayne/dex/internal/db/sqlc"
	"github.com/vaayne/dex/internal/server"
	"github.com/vaayne/dex/internal/worker"
	"github.com/vaayne/dex/internal/workerbin"
)

// DefaultAgentID is the agent that the web UI talks to.
const DefaultAgentID = "default"

// Config configures an App.
type Config struct {
	DataDir  string // control.sqlite and agents/<id>/
	CacheDir string // the installed worker binary; also the worker's working directory
	Model    worker.ModelRef
	// WorkerEnv is the worker's complete environment, without DEX_DATA_DIR. Use WorkerEnv() for
	// the default.
	WorkerEnv []string
}

// App is a running control process.
type App struct {
	db      *sql.DB
	handler http.Handler
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Start opens the database, makes sure the default agent exists, and starts the worker.
func Start(ctx context.Context, cfg Config) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	conn, err := db.Open(ctx, filepath.Join(cfg.DataDir, "control.sqlite"))
	if err != nil {
		return nil, err
	}
	q := sqlc.New(conn)
	err = q.UpsertAgent(ctx, sqlc.UpsertAgentParams{
		ID:            DefaultAgentID,
		Name:          "Dex",
		ModelProvider: cfg.Model.Provider,
		ModelID:       cfg.Model.ModelID,
		CreatedAt:     time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	path, err := worker.Install(cfg.CacheDir, workerbin.Gzip)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}

	var chats *chat.Service
	w := worker.New(worker.Config{
		Path:      path,
		Dir:       cfg.CacheDir,
		Env:       append(cfg.WorkerEnv, "DEX_DATA_DIR="+cfg.DataDir),
		OnSettled: func(ctx context.Context, s worker.Settled) { chats.HandleSettled(ctx, s) },
		OnStart:   func(ctx context.Context) { chats.Replay(ctx) },
	})
	chats = chat.New(q, w, time.Now)

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a := &App{db: conn, handler: server.New(chats, DefaultAgentID), cancel: cancel}
	a.wg.Go(func() { _ = w.Run(runCtx) })
	return a, nil
}

// Handler serves the API and the web UI.
func (a *App) Handler() http.Handler { return a.handler }

// Close stops the worker, waits for it to close its storage, and closes the database.
func (a *App) Close() error {
	a.cancel()
	a.wg.Wait()
	return a.db.Close()
}

// WorkerEnv selects the variables of environ that the worker needs: the basic process variables,
// provider credentials and endpoints (*_API_KEY, *_BASE_URL), and DEX_FAKE_MODEL. The worker gets
// nothing else from control's environment.
func WorkerEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case name == "PATH", name == "HOME", name == "TMPDIR", name == "LANG", name == "USER", name == "SHELL",
			name == "DEX_FAKE_MODEL",
			strings.HasSuffix(name, "_API_KEY"), strings.HasSuffix(name, "_BASE_URL"):
			env = append(env, kv)
		}
	}
	return env
}
