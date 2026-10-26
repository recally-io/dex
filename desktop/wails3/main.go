//go:build darwin

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/host"
	"github.com/recally-io/dex/internal/service"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/web"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const (
	appName       = "Dex"
	appIdentifier = "io.recally.dex"
)

// version is the release tag. The release build sets it with -X; the desktop module cannot
// read the tag from its build info, because it builds dex from a replace directive.
var version = "desktop-dev"

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
		RawMessageHandler: handleFolderMessage(backendURL),
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
	options := application.WebviewWindowOptions{
		Name:       "main",
		Title:      appName,
		Width:      1280,
		Height:     860,
		MinWidth:   900,
		MinHeight:  600,
		StartState: application.WindowStateNormal,
		URL:        backendURL,
		// The window shows after restoreDesktopWindow places it.
		Hidden: true,
		// The default macOS title bar owns the traffic lights, the title and window drags, so no page
		// has to leave room for them. A title bar drawn by the web UI waits for one shared header.
		Mac: application.MacWindow{TitleBar: application.MacTitleBarDefault},
	}
	mainWindow := native.Window.NewWithOptions(options)
	native.Menu.SetApplicationMenu(desktopMenu(mainWindow))
	// Wails queues ExecJS until the page reports ready, which only its full runtime does. The page
	// comes from the Dex backend and does not load that runtime, so the shell reports ready itself.
	mainWindow.RegisterHook(events.Mac.WebViewDidFinishNavigation, func(_ *application.WindowEvent) {
		mainWindow.HandleMessage("wails:runtime:ready")
	})
	// The screen list is empty until the application starts, so the saved frame is checked then.
	native.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		statePath := desktopWindowStatePath()
		restoreDesktopWindow(mainWindow, statePath, native)
		mainWindow.Show()
		// Saving starts after the restore, so the default frame never replaces the saved one.
		save := func(_ *application.WindowEvent) { saveDesktopWindowState(mainWindow, statePath) }
		for _, event := range []events.WindowEventType{
			events.Common.WindowDidMove, events.Common.WindowDidResize,
			events.Common.WindowMaximise, events.Common.WindowUnMaximise, events.Common.WindowClosing,
		} {
			mainWindow.OnWindowEvent(event, save)
		}
	})

	if err := native.Run(); err != nil {
		return err
	}
	if closer == nil {
		return nil
	}
	return closer.Close()
}

func desktopWindowStatePath() string {
	config, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "dex", "window.json")
	}
	return filepath.Join(config, appName, "window.json")
}

func loadDesktopWindowState(path string, native *application.App) (windowState, bool) {
	// Wails fills its screen list in another listener of the launch event, and listeners run
	// concurrently. Wait for the list; if it takes longer, the window keeps the default frame.
	for deadline := time.Now().Add(time.Second); len(native.Screen.GetAll()) == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	screens := make([]screenRect, 0, len(native.Screen.GetAll()))
	for _, screen := range native.Screen.GetAll() {
		screens = append(screens, screenRect{X: screen.WorkArea.X, Y: screen.WorkArea.Y, Width: screen.WorkArea.Width, Height: screen.WorkArea.Height})
	}
	return loadWindowState(path, screens)
}

func restoreDesktopWindow(window application.Window, path string, native *application.App) {
	state, ok := loadDesktopWindowState(path, native)
	if !ok {
		return
	}
	window.SetSize(state.Width, state.Height)
	window.SetPosition(state.X, state.Y)
	if state.Maximised {
		window.Maximise()
	}
}

func saveDesktopWindowState(window application.Window, path string) {
	x, y := window.Position()
	w, h := window.Size()
	if err := recordWindowState(path, windowState{X: x, Y: y, Width: w, Height: h, Maximised: window.IsMaximised()}); err != nil {
		slog.Warn("save dex window state", "err", err)
	}
}

func desktopMenu(window application.Window) *application.Menu {
	menu := application.NewMenu()
	appMenu := menu.AddSubmenu(appName)
	appMenu.AddRole(application.About)
	appMenu.Add("Settings…").SetAccelerator("cmd+,").OnClick(func(_ *application.Context) {
		window.ExecJS("window.dispatchEvent(new CustomEvent('dex:settings'))")
	})
	appMenu.AddSeparator()
	appMenu.AddRole(application.Hide)
	appMenu.AddRole(application.HideOthers)
	appMenu.AddRole(application.UnHide)
	appMenu.AddSeparator()
	appMenu.AddRole(application.Quit)
	file := menu.AddSubmenu("File")
	file.Add("New Chat").SetAccelerator("cmd+n").OnClick(func(_ *application.Context) {
		window.ExecJS("window.dispatchEvent(new CustomEvent('dex:new-chat'))")
	})
	file.AddRole(application.CloseWindow)
	edit := menu.AddSubmenu("Edit")
	edit.AddRole(application.Undo)
	edit.AddRole(application.Redo)
	edit.AddSeparator()
	edit.AddRole(application.Cut)
	edit.AddRole(application.Copy)
	edit.AddRole(application.Paste)
	edit.AddRole(application.SelectAll)
	view := menu.AddSubmenu("View")
	view.AddRole(application.Reload)
	windowMenu := menu.AddSubmenu("Window")
	windowMenu.AddRole(application.Minimise)
	windowMenu.AddRole(application.Zoom)
	return menu
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
			Version: version,
		},
	})
	if err != nil {
		return "", nil, err
	}
	slog.Info("started dex desktop backend", "url", backend.URL(), "dataDir", cfg.DataDir)
	return backend.URL(), &desktopCloser{close: backend.Close}, nil
}
