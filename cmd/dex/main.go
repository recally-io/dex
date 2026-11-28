// Command dex runs the HTTP API, web UI, and native bot runtime in one process.
// Bot settings are in the web UI; the optional environment variables
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
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/service"
	"github.com/recally-io/dex/internal/sysops"
	"github.com/recally-io/dex/internal/update"
	"github.com/recally-io/dex/plugins/email"
	"github.com/recally-io/dex/plugins/github"
	"github.com/recally-io/dex/plugins/webhook"
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
	logBuf := sysops.NewLogBuffer(0)
	// Wrap a fresh TextHandler writing directly to os.Stderr rather than slog.Default().Handler():
	// Go's default handler bridges back through log.Logger, which deadlocks on outMu when SetDefault routes log back into slog.
	slog.SetDefault(slog.New(sysops.TeeHandler(slog.NewTextHandler(os.Stderr, nil), logBuf)))

	addr := cfg.Addr()
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	closedListener := false
	closeListener := func() {
		if !closedListener {
			closedListener = true
			_ = listener.Close()
		}
	}
	defer closeListener()

	telegramURL, closeTelegram := testTelegramAPI()
	defer closeTelegram()
	catalogURL, closeCatalog := testCatalogAPI()
	defer closeCatalog()
	restartCh := make(chan string, 1)
	a, err := app.Start(ctx, app.Config{
		TelegramAPIURL:  telegramURL,
		SkillCatalogURL: catalogURL,
		McpRegistryURL:  catalogURL,
		DataDir:         cfg.DataDir,
		ToolEnv:         app.ToolEnv(os.Environ()),
		LoginEnv:        testLoginEnv(),
		FakeModel:       testFakeModel(),
		Version:         version(),
		Token:           token,
		RequireToken:    requireToken,
		Logs:            logBuf,
		RequestRestart: func(execPath string) {
			select {
			case restartCh <- execPath:
			default:
			}
		},
		// The plugins of this build. The owner turns each one on or off in the settings.
		Plugins: []plugin.Factory{github.New, webhook.New, email.New},
	})
	if err != nil {
		return err
	}

	serveCtx, cancelServe := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelServe()
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return serveCtx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(listener) }()
	slog.Info("dex is running", "url", fmt.Sprintf("http://%s", addr))
	switch {
	case token != "":
		slog.Info("the API requires DEX_TOKEN")
	case requireToken && !cfg.Loopback():
		slog.Warn("the API accepts network connections over plain HTTP and requires the stored token; print it with: dex token")
	case requireToken:
		slog.Info("the API requires the stored token; print it with: dex token")
	}

	var restartExec string
	restarting := false
	select {
	case err = <-errc:
	case <-ctx.Done():
		cancelServe()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = srv.Shutdown(shutdown)
	case restartExec = <-restartCh:
		restarting = true
		cancelServe()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = srv.Shutdown(shutdown)
	}
	closeListener()
	closeErr := errors.Join(err, a.Close()) //nolint:contextcheck // Close cancels the app-owned context and waits for cleanup.
	if !restarting {
		return closeErr
	}
	if closeErr != nil {
		slog.Warn("shutdown before re-exec reported an error", "err", closeErr)
	}
	upgraded := restartExec != ""
	if restartExec == "" {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return fmt.Errorf("resolve executable for restart: %w", exeErr)
		}
		restartExec = exe
	}
	if execErr := update.Reexec(restartExec, os.Args, os.Environ()); execErr != nil {
		if upgraded {
			if restoreErr := update.RestorePrevious(restartExec); restoreErr != nil {
				slog.Error("restore previous executable after re-exec failure", "err", restoreErr)
			}
		}
		return fmt.Errorf("re-exec %s: %w", restartExec, execErr)
	}
	return nil
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
