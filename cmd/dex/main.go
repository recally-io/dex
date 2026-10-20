// Command dex runs the HTTP API, web UI, and native agent runtime in one process.
// Agent settings are in the web UI; the optional environment variables
// are DEX_DATA_DIR (default ~/.dex), DEX_ADDR (default 127.0.0.1:8787), and DEX_TOKEN.
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
	"runtime/debug"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/service"
	"golang.org/x/mod/module"
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

func serve(ctx context.Context, cfg service.Config, token string, requireToken bool) error {
	addr := cfg.Addr()
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	defer listener.Close()

	a, err := app.Start(ctx, app.Config{
		DataDir:      cfg.DataDir,
		ToolEnv:      app.ToolEnv(os.Environ()),
		FakeModel:    testFakeModel(),
		Version:      version(),
		Token:        token,
		RequireToken: requireToken,
	})
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(listener) }()
	slog.Info("dex is running", "url", fmt.Sprintf("http://%s", addr))
	switch {
	case token != "":
		slog.Info("the API requires DEX_TOKEN")
	case requireToken:
		slog.Warn("the API accepts network connections over plain HTTP and requires the stored token; print it with: dex token")
	}

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
		if module.IsPseudoVersion(v) {
			return "dev"
		}
		return v
	}
	return "dev"
}
