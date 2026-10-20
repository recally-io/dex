//go:build darwin

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/host"
	"github.com/recally-io/dex/internal/service"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/web"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	appName       = "Dex"
	appIdentifier = "io.recally.dex"
)

type desktopCloser struct {
	close func(context.Context) error
	once  sync.Once
	err   error
}

func (c *desktopCloser) Close() error {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.err = c.close(ctx)
	})
	return c.err
}

func main() {
	if err := run(); err != nil {
		slog.Error("dex desktop stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := desktopConfig()
	if err != nil {
		return err
	}

	backendURL, closer, err := startBackend(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	native := application.New(application.Options{
		Name: appName,
		Assets: application.AssetOptions{
			Handler: web.Handler(),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appIdentifier,
			ExitCode: 1,
		},
		OnShutdown: func() {
			if closer == nil {
				return
			}
			if err := closer.Close(); err != nil {
				slog.Error("close dex backend", "err", err)
			}
		},
	})
	native.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:       "main",
		Title:      appName,
		Width:      1280,
		Height:     860,
		MinWidth:   900,
		MinHeight:  600,
		StartState: application.WindowStateNormal,
		URL:        backendURL,
	})

	if err := native.Run(); err != nil {
		return err
	}
	if closer == nil {
		return nil
	}
	return closer.Close()
}

func desktopConfig() (service.Config, error) {
	cfg, err := service.Defaults()
	if err != nil {
		return service.Config{}, err
	}
	manager, err := service.New()
	if err != nil {
		return cfg, nil
	}
	stored, err := manager.Read()
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return service.Config{}, err
	}
	return stored, nil
}

func startBackend(cfg service.Config) (string, *desktopCloser, error) {
	probeCfg := cfg
	probeCfg.Host = "0.0.0.0"
	probeCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if health, err := service.Probe(probeCtx, probeCfg); err == nil && health.DataDir == cfg.DataDir {
		token, err := settings.StoredToken(context.Background(), cfg.DataDir)
		if err != nil {
			return "", nil, err
		}
		proxy, err := host.StartProxy(context.Background(), cfg.URL(), token)
		if err != nil {
			return "", nil, err
		}
		slog.Info("using existing dex service through desktop proxy", "url", proxy.URL(), "dataDir", cfg.DataDir)
		return proxy.URL(), &desktopCloser{close: proxy.Close}, nil
	}

	backend, err := host.Start(context.Background(), host.Config{
		Addr: "127.0.0.1:0",
		App: app.Config{
			DataDir: cfg.DataDir,
			ToolEnv: app.ToolEnv(os.Environ()),
			Version: "desktop-dev",
		},
	})
	if err != nil {
		return "", nil, err
	}
	slog.Info("started dex desktop backend", "url", backend.URL(), "dataDir", cfg.DataDir)
	return backend.URL(), &desktopCloser{close: backend.Close}, nil
}
