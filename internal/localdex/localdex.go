// Package localdex starts Dex without an HTTP server.
package localdex

import (
	"context"
	"net/http"
	"os"
	"sync"

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

// Instance owns the workspace runtime. Call Close when the workspace is no longer in use.
type Instance struct {
	runtime  *app.App
	lifetime context.Context
	stop     context.CancelFunc
	gate     sync.Mutex
	requests sync.WaitGroup
	close    func() error
}

// Start starts the services and embeds the API and web UI in the caller's process.
// The handler has no sign-in. Do not expose it to a network.
func Start(ctx context.Context, cfg Config) (*Instance, error) {
	config := app.Config{
		RequireToken: false,
		Token:        "",
		DataDir:      cfg.DataDir,
		Version:      cfg.Version,
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
	runtime, err := app.Start(ctx, config)
	if err != nil {
		return nil, err
	}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	i := &Instance{runtime: runtime, lifetime: lifetime, stop: stop}
	i.close = sync.OnceValue(i.shutdown)
	return i, nil
}

// Handler serves the local API and embedded web UI directly.
func (i *Instance) Handler() http.Handler { return http.HandlerFunc(i.serve) }

func (i *Instance) serve(w http.ResponseWriter, r *http.Request) {
	i.gate.Lock()
	if i.lifetime.Err() != nil {
		i.gate.Unlock()
		http.Error(w, "workspace is closed", http.StatusServiceUnavailable)
		return
	}
	i.requests.Add(1)
	i.gate.Unlock()
	defer i.requests.Done()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(i.lifetime, cancel) //nolint:contextcheck // Also cancel requests when the instance closes.
	defer stop()
	i.runtime.Handler().ServeHTTP(w, r.WithContext(ctx))
}

func (i *Instance) shutdown() error {
	i.gate.Lock()
	i.stop()
	i.gate.Unlock()
	i.requests.Wait()
	return i.runtime.Close()
}

// Close stops the services and releases the workspace data.
func (i *Instance) Close() error { return i.close() }
