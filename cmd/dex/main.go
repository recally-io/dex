// Command dex runs the dex control process: the HTTP API and web UI, with the embedded agent
// worker as a child process. All settings are in the web UI; the optional environment variables
// are DEX_DATA_DIR (default ~/.dex) and DEX_ADDR (default 127.0.0.1:8787).
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/vaayne/dex/internal/app"
)

func main() {
	if err := run(); err != nil {
		slog.Error("dex", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	addr := cmp.Or(os.Getenv("DEX_ADDR"), "127.0.0.1:8787")

	a, err := app.Start(ctx, app.Config{
		DataDir:   cmp.Or(os.Getenv("DEX_DATA_DIR"), filepath.Join(home, ".dex")),
		CacheDir:  filepath.Join(cache, "dex"),
		WorkerEnv: app.WorkerEnv(os.Environ()),
		Version:   version(),
	})
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("dex is running", "url", fmt.Sprintf("http://%s", addr))

	select {
	case err = <-errc:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = srv.Shutdown(shutdown)
	}
	return errors.Join(err, a.Close())
}

// version is the module version of the build, or "dev" for a build from a checkout.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
