// Command dex runs the dex control process: the HTTP API and web UI, with the embedded agent
// worker as a child process.
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
	"strings"
	"syscall"
	"time"

	"github.com/vaayne/dex/internal/app"
	"github.com/vaayne/dex/internal/worker"
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
	// DEX_MODEL is "<provider>/<model ID>"; the model ID may contain "/".
	provider, modelID, ok := strings.Cut(os.Getenv("DEX_MODEL"), "/")
	if !ok || provider == "" || modelID == "" {
		return errors.New(`set DEX_MODEL to "<provider>/<model ID>", for example "openai-compatible/codex/gpt-6-luna"`)
	}
	addr := cmp.Or(os.Getenv("DEX_ADDR"), "127.0.0.1:8787")

	a, err := app.Start(ctx, app.Config{
		DataDir:   cmp.Or(os.Getenv("DEX_DATA_DIR"), filepath.Join(home, ".dex")),
		CacheDir:  filepath.Join(cache, "dex"),
		Model:     worker.ModelRef{Provider: provider, ModelID: modelID},
		WorkerEnv: app.WorkerEnv(os.Environ()),
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
