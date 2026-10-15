package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vaayne/dex/internal/service"
	"github.com/vaayne/dex/internal/update"
)

var errUsage = errors.New("usage error")

func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet("dex "+name, flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errors.Join(errUsage, err)
	}
	if f.NArg() > 0 {
		f.Usage()
		return fmt.Errorf("%w: unexpected argument %q", errUsage, f.Arg(0))
	}
	return nil
}
func usage() {
	fmt.Fprintln(os.Stderr, "Usage: dex [serve|version|service|update|open|doctor] [options]\n  No command: serve in the foreground.\n  dex service install: start now and at login.\n  Use dex <command> --help for options.")
}
func runFlags(f *flag.FlagSet, c *service.Config) {
	f.StringVar(&c.Host, "host", c.Host, "Loopback host (127.0.0.0/8 or ::1)")
	f.IntVar(&c.Port, "port", c.Port, "HTTP port")
	f.StringVar(&c.DataDir, "data-dir", c.DataDir, "Data directory (does not move existing data)")
}
func configured(f *flag.FlagSet, args []string, c *service.Config) error {
	runFlags(f, c)
	if err := parse(f, args); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return errors.Join(errUsage, err)
	}
	return nil
}
func dispatch(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 0 || strings.HasPrefix(args[0], "-") && args[0] != "--help" && args[0] != "-h" && args[0] != "--version" {
		args = append([]string{"serve"}, args...)
	}
	name := args[0]
	args = args[1:]
	switch name {
	case "help", "--help", "-h":
		usage()
		return nil
	case "version", "--version":
		return versionCommand(args)
	case "serve":
		return serveCommand(ctx, args)
	case "service":
		return serviceCommand(ctx, args)
	case "update":
		return updateCommand(ctx, args)
	case "open", "doctor":
		return localCommand(ctx, name, args)
	default:
		usage()
		return fmt.Errorf("%w: unknown command %q", errUsage, name)
	}
}
func serviceCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: dex service install|uninstall|start|stop|restart|status|logs [options]")
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	action := args[0]
	args = args[1:]
	m, err := service.New()
	if err != nil {
		return err
	}
	switch action {
	case "install":
		c, err := m.Read()
		if errors.Is(err, os.ErrNotExist) {
			c, err = service.Defaults()
		}
		if err != nil {
			return err
		}
		f := flags("service install")
		f.BoolVar(&c.AutoUpdate, "auto-update", c.AutoUpdate, "Enable daily updates (requires stored gh credentials)")
		f.StringVar(&c.Channel, "channel", c.Channel, "Update channel: stable or prerelease")
		if err := configured(f, args, &c); err != nil {
			return err
		}
		c.Executable, err = service.Executable()
		if err != nil {
			return err
		}
		c.Path = os.Getenv("PATH")
		if c.AutoUpdate {
			if _, err := update.Access(ctx, true); err != nil {
				return err
			}
		}
		status, err := m.Status(ctx)
		if err != nil {
			return err
		}
		// A different listener must not make installation appear healthy.
		if !status.Running || status.Config.Addr() != c.Addr() {
			listener, err := new(net.ListenConfig).Listen(ctx, "tcp", c.Addr())
			if err != nil {
				return fmt.Errorf("address %s is in use; stop its listener or choose another --port: %w", c.Addr(), err)
			}
			_ = listener.Close()
		}
		if err := m.Install(ctx, c); err != nil {
			return fmt.Errorf("install: %w; logs: %s", err, m.LogPath())
		}
		if err := service.Ready(ctx, c, version()); err != nil {
			return fmt.Errorf("%w; logs: %s", err, m.LogPath())
		}
		fmt.Printf("dex is running: %s\nBinary: %s\nLogs: dex service logs\nLog location: %s\nStatus: dex service status\nUninstall: dex service uninstall\n", c.URL(), c.Executable, m.LogPath())
		if runtime.GOOS == "linux" {
			printLinger(ctx)
		}
		return nil
	case "status":
		f := flags("service status")
		asJSON := f.Bool("json", false, "Print stable JSON fields")
		if err := parse(f, args); err != nil {
			return err
		}
		s, err := m.Status(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.MarshalWrite(os.Stdout, s)
		}
		fmt.Printf("Installed: %t\nRunning: %t\nHealthy: %t\nPID: %d\nURL: %s\nData directory: %s\nExecutable: %s\nVersion: %s\nAuto-update: %t (%s)\nDefinition: %s\nLogs: %s\n", s.Installed, s.Running, s.Healthy, s.PID, s.Config.URL(), s.Config.DataDir, s.Config.Executable, s.Version, s.Config.AutoUpdate, s.Config.Channel, s.Definition, s.Logs)
		return nil
	case "logs":
		f := flags("service logs")
		n := f.Int("n", 100, "Number of log lines")
		follow := f.Bool("f", false, "Follow the log")
		if err := parse(f, args); err != nil {
			return err
		}
		if *n < 0 {
			return fmt.Errorf("%w: -n must not be negative", errUsage)
		}
		tool := "journalctl"
		argv := []string{"--user", "-u", "dex", "--no-pager", "-n", strconv.Itoa(*n)}
		if runtime.GOOS == "darwin" {
			tool = "tail"
			argv = []string{"-n", strconv.Itoa(*n)}
			if *follow {
				argv = append(argv, "-F")
			}
			argv = append(argv, m.LogPath())
		} else if *follow {
			argv = append(argv, "-f")
		}
		//nolint:gosec // G204: fixed log tools, with arguments and no shell.
		cmd := exec.CommandContext(ctx, tool, argv...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "uninstall", "start", "stop", "restart":
		f := flags("service " + action)
		if err := parse(f, args); err != nil {
			return err
		}
		if action == "uninstall" {
			return m.Uninstall(ctx)
		}
		if action == "stop" {
			return m.Stop(ctx)
		}
		c, err := m.Read()
		if err != nil {
			return err
		}
		if action == "start" {
			err = m.Start(ctx)
		} else {
			err = m.Restart(ctx)
		}
		if err != nil {
			return err
		}
		// The stored executable can differ from the CLI used for this command.
		v, err := service.BinaryVersion(ctx, c.Executable)
		if err != nil {
			return err
		}
		if err := service.Ready(ctx, c, v); err != nil {
			return fmt.Errorf("%w; logs: %s", err, m.LogPath())
		}
		return nil
	default:
		fmt.Fprintln(os.Stderr, "Usage: dex service install|uninstall|start|stop|restart|status|logs [options]")
		return fmt.Errorf("%w: unknown service command %q", errUsage, action)
	}
}
func lingering(ctx context.Context) (bool, error) {
	//nolint:gosec // G204: fixed command and numeric current-user ID, without a shell.
	b, err := exec.CommandContext(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "-p", "Linger", "--value").Output()
	return strings.TrimSpace(string(b)) == "yes", err
}
func printLinger(ctx context.Context) {
	if enabled, err := lingering(ctx); err == nil && !enabled {
		fmt.Printf("Hint: loginctl enable-linger %d keeps dex running after logout and at boot.\n", os.Getuid())
	}
}
func updateCommand(ctx context.Context, args []string) error {
	f := flags("update")
	check := f.Bool("check", false, "Check without installing")
	channel := f.String("channel", "", "stable or prerelease")
	tag := f.String("version", "", "Exact release tag")
	scheduled := f.Bool("scheduled", false, "Skip a busy instance (for daily updates)")
	if err := parse(f, args); err != nil {
		return err
	}
	if *channel != "" && *tag != "" {
		return fmt.Errorf("%w: --channel and --version are exclusive", errUsage)
	}
	m, err := service.New()
	if err != nil {
		return err
	}
	if *channel == "" {
		*channel = "stable"
		if c, e := m.Read(); e == nil {
			*channel = c.Channel
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if *channel != "stable" && *channel != "prerelease" {
		return fmt.Errorf("%w: channel must be stable or prerelease", errUsage)
	}
	path, err := os.Executable()
	if err != nil {
		return err
	}
	u, err := update.New(version(), path)
	if err != nil {
		return err
	}
	return u.Run(ctx, update.Options{Check: *check, Channel: *channel, Tag: *tag, Scheduled: *scheduled}, m, os.Stdout)
}

// doctor only observes the installed instance. It never opens its storage or starts a worker.
func doctor(ctx context.Context, m service.Manager, c service.Config) error {
	failed := false
	report := func(level, name, detail string) {
		fmt.Printf("%s %s: %s\n", level, name, detail)
		if level == "fail" {
			failed = true
		}
	}
	s, err := m.Status(ctx)
	switch {
	case err != nil:
		report("fail", "service", err.Error())
	case !s.Installed:
		report("warn", "service", "not installed")
	default:
		report("ok", "service", "installed")
		if s.Running {
			report("ok", "running", s.Version)
		} else {
			report("warn", "running", "stopped")
		}
	}
	var h service.Health
	if s.Installed && s.Config.Addr() == c.Addr() {
		if s.Health != nil {
			h = *s.Health
			err = nil
		} else {
			err = errors.New("installed service did not answer health")
		}
	} else {
		h, err = service.Probe(ctx, c)
	}
	if err == nil {
		report("ok", "address", c.Addr()+" answers as dex "+h.Version)
		if h.DataDir != c.DataDir {
			report("fail", "data directory", "running instance uses "+h.DataDir)
		}
		if h.WorkerReady {
			report("ok", "worker", "ping answered")
		} else {
			report("fail", "worker", "ping did not answer")
		}
	} else {
		conn, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", c.Addr())
		if dialErr == nil {
			_ = conn.Close()
			report("fail", "address", c.Addr()+" is occupied by a listener that does not answer as dex; inspect it with lsof or ss")
		} else {
			report("warn", "address", "not listening")
			report("skipped", "worker", "no running dex instance")
		}
	}
	info, err := os.Stat(c.DataDir)
	switch {
	case err != nil:
		report("warn", "data directory", err.Error())
	case !info.IsDir() || info.Mode().Perm()&0077 != 0:
		report("fail", "data directory", "must be a directory private to this user (0700)")
	default:
		report("ok", "data directory", c.DataDir+" (0700)")
	}
	if !s.Installed {
		report("skipped", "PATH", "no installed definition")
	} else {
		dirs := filepath.SplitList(s.Config.Path)
		common := false
		for _, dir := range dirs {
			if dir == "/usr/local/bin" || dir == "/opt/homebrew/bin" || strings.HasSuffix(dir, "/.local/bin") {
				common = true
			}
		}
		if common {
			report("ok", "PATH", "contains common user tool directories")
		} else {
			report("warn", "PATH", "no /usr/local/bin, /opt/homebrew/bin, or ~/.local/bin; reinstall from your shell if tools are missing")
		}
	}
	if runtime.GOOS == "linux" {
		enabled, err := lingering(ctx)
		switch {
		case err != nil:
			report("warn", "lingering", err.Error())
		case enabled:
			report("ok", "lingering", "enabled")
		default:
			report("warn", "lingering", "off; dex stops after logout")
		}
	}
	source, err := update.Access(ctx, false)
	if err != nil {
		report("warn", "update access", err.Error())
	} else {
		report("ok", "update access", source+"; release API reachable")
	}
	if failed {
		return errors.New("doctor found a failure")
	}
	return nil
}

func versionCommand(args []string) error {
	f := flags("version")
	if err := parse(f, args); err != nil {
		return err
	}
	path, err := os.Executable()
	if err != nil {
		return err
	}
	commit := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				commit = setting.Value
			}
		}
	}
	fmt.Printf("dex %s\nCommit: %s\nPlatform: %s/%s\nExecutable: %s\n", version(), commit, runtime.GOOS, runtime.GOARCH, path)
	return nil
}

func serveCommand(ctx context.Context, args []string) error {
	c, err := service.Defaults()
	if err != nil {
		return errors.Join(errUsage, err)
	}
	err = configured(flags("serve"), args, &c)
	if err != nil {
		return err
	}
	return serve(ctx, c)
}

func localCommand(ctx context.Context, name string, args []string) error {
	c, err := service.Defaults()
	if err != nil {
		return errors.Join(errUsage, err)
	}
	m, managerErr := service.New()
	if managerErr == nil {
		if stored, e := m.Read(); e == nil {
			c = stored
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	f := flags(name)
	printOnly := false
	if name == "open" {
		f.BoolVar(&printOnly, "print", false, "Print the URL without opening a browser")
	}
	if err := configured(f, args, &c); err != nil {
		return err
	}
	if name == "doctor" {
		if managerErr != nil {
			return managerErr
		}
		return doctor(ctx, m, c)
	}
	fmt.Println(c.URL())
	if printOnly {
		return nil
	}
	tool := "xdg-open"
	if runtime.GOOS == "darwin" {
		tool = "open"
	}
	//nolint:gosec // G204: fixed browser opener and validated loopback URL, without a shell.
	if err := exec.CommandContext(ctx, tool, c.URL()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Could not open the browser; use the URL above.")
	}
	return nil
}
