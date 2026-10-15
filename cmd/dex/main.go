// Command dex runs the dex control process: the HTTP API and web UI, with the embedded agent
// worker as a child process. Agent settings are in the web UI; the optional environment variables
// are DEX_DATA_DIR (default ~/.dex) and DEX_ADDR (default 127.0.0.1:8787).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/vaayne/dex/internal/app"
	"github.com/vaayne/dex/internal/service"
)

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func serve(ctx context.Context, cfg service.Config) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	addr := cfg.Addr()
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	defer listener.Close()

	a, err := app.Start(ctx, app.Config{
		DataDir:   cfg.DataDir,
		CacheDir:  filepath.Join(cache, "dex"),
		WorkerEnv: append(app.WorkerEnv(os.Environ()), testWorkerEnv()...),
		Version:   version(),
	})
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(listener) }()
	slog.Info("dex is running", "url", fmt.Sprintf("http://%s", addr))

	select {
	case err = <-errc:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = srv.Shutdown(shutdown)
	}
	return errors.Join(err, a.Close()) //nolint:contextcheck // Close cancels the app-owned context and waits for cleanup.
}

var buildVersion string

// version is the module version of the build, or "dev" for a build from a checkout.
func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				return "dev"
			}
		}
		v := info.Main.Version
		// Go can label a checkout with a pseudo-version instead of (devel).
		core, _, _ := strings.Cut(v, "+")
		prefix, revision, ok := strings.CutLast(core, "-")
		if ok && len(revision) >= 12 && len(prefix) >= 14 {
			if _, err := time.Parse("20060102150405", prefix[len(prefix)-14:]); err == nil {
				return "dev"
			}
		}
		return v
	}
	return "dev"
}
