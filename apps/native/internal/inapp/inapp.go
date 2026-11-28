// Package inapp starts Dex without an HTTP server.
package inapp

import (
	"context"
	"net/http"
	"os"
	"sync"

	"github.com/recally-io/dex/apps/web"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/sysops"
	"github.com/recally-io/dex/plugins/builtin"
)

// Config selects the workspace data, version, and tool environment.
type Config struct {
	DataDir string
	Version string
	Mobile  bool
}

// Runtime runs the workspace services. Call Close when the workspace is no longer in use.
type Runtime struct {
	app      *app.App
	lifetime context.Context
	stop     context.CancelFunc
	gate     sync.Mutex
	requests sync.WaitGroup
	close    func() error
}

// Start starts the services and embeds the API and web UI in the caller's process.
// The handler has no sign-in. Do not expose it to a network.
func Start(ctx context.Context, cfg Config) (*Runtime, error) {
	config := app.Config{
		RequireToken: false,
		Token:        "",
		DataDir:      cfg.DataDir,
		Version:      cfg.Version,
		WebHandler:   web.Handler(),
		Logs:         sysops.NewLogBuffer(0),
		Plugins:      builtin.Factories(),
	}
	if cfg.Mobile {
		config.Home = cfg.DataDir
		config.ToolEnv = map[string]string{"HOME": cfg.DataDir, "TMPDIR": os.TempDir()}
		config.LoginEnv = nil
	} else {
		config.ToolEnv = app.ToolEnv(os.Environ())
	}
	a, err := app.Start(ctx, config)
	if err != nil {
		return nil, err
	}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	rt := &Runtime{app: a, lifetime: lifetime, stop: stop}
	rt.close = sync.OnceValue(rt.shutdown)
	return rt, nil
}

// Handler serves the local API and embedded web UI directly.
func (rt *Runtime) Handler() http.Handler { return http.HandlerFunc(rt.serve) }

func (rt *Runtime) serve(w http.ResponseWriter, r *http.Request) {
	rt.gate.Lock()
	if rt.lifetime.Err() != nil {
		rt.gate.Unlock()
		http.Error(w, "workspace is closed", http.StatusServiceUnavailable)
		return
	}
	rt.requests.Add(1)
	rt.gate.Unlock()
	defer rt.requests.Done()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(rt.lifetime, cancel) //nolint:contextcheck // Also cancel requests when the runtime closes.
	defer stop()
	rt.app.Handler().ServeHTTP(w, r.WithContext(ctx))
}

func (rt *Runtime) shutdown() error {
	rt.gate.Lock()
	rt.stop()
	rt.gate.Unlock()
	rt.requests.Wait()
	return rt.app.Close()
}

// Close stops the services and releases the workspace data.
func (rt *Runtime) Close() error { return rt.close() }
