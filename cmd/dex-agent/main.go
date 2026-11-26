// dex-agent connects an execution host to Dex, as the account that installs it or as a dedicated
// account of a shared host.
package main

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/loginenv"
	"github.com/recally-io/dex/internal/runner/outbound"
)

// device is the enrolled state. Dex identifies this machine by the token that enrollment
// returned, and stores only its digest. Mode describes the local install.
type device struct {
	Origin   string `json:"origin"`
	RunnerID string `json:"runnerId"`
	Token    string `json:"token"`
	Mode     string `json:"mode,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := command(ctx, os.Args[1:], os.Stdin)
	stop()
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func command(ctx context.Context, args []string, input io.Reader) error {
	if len(args) == 0 {
		return errors.New("usage: dex-agent enroll -url https://dex.example.com -id RUNNER_ID -code CODE [-mode user] | dex-agent run [-no-auto-update] | dex-agent status | dex-agent version")
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Printf("dex-agent %s\n", agentVersion())
		return nil
	}
	private, err := defaultState()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	state := flags.String("state", private, "private machine state directory")
	origin, id, code, mode := new(string), new(string), new(string), new(string)
	noAutoUpdate := new(bool)
	switch args[0] {
	case "enroll":
		origin = flags.String("url", "", "Dex address, for example https://dex.example.com")
		id = flags.String("id", "", "Runner ID")
		code = flags.String("code", "", "one-time enrollment code from the install command")
		flags.StringVar(mode, "mode", modeDedicated, "run account mode: user or dedicated")
	case "run":
		noAutoUpdate = flags.Bool("no-auto-update", false, "disable automatic version alignment with the Dex server")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch args[0] {
	case "enroll":
		if *code == "" {
			line, err := bufio.NewReader(io.LimitReader(input, 4096)).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			*code = strings.TrimSpace(line)
		}
		return enroll(ctx, *state, *origin, *id, *code, *mode)
	case "run":
		return run(ctx, *state, *noAutoUpdate)
	case "status":
		return status(ctx, *state, os.Stdout)
	default:
		return errors.New("expected enroll, run, status, or version")
	}
}

// endpoint builds one machine route. A session uses the WebSocket scheme of the origin.
// defaultState returns the directory that holds the machine token and its workspace. It follows
// XDG_STATE_HOME, and falls back to the platform directory for private state.
func defaultState() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "dex-agent"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		// macOS has no XDG directory; Application Support is its home for private user state.
		return filepath.Join(home, "Library", "Application Support", "dex-agent"), nil
	}
	return filepath.Join(home, ".local", "state", "dex-agent"), nil
}

func endpoint(origin, id, action string, tail ...string) (string, error) {
	u, err := url.Parse(origin)
	// A loopback address may use plain HTTP, so a developer can test the flow without a proxy.
	secure := u != nil && (u.Scheme == "https" || u.Scheme == "http" && u.Hostname() == "127.0.0.1")
	if err != nil || !secure || u.User != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the machine address must be an HTTPS origin without a path or query")
	}
	if id == "" || strings.ContainsAny(id, "/\\?#.%") {
		return "", errors.New("invalid Runner ID")
	}
	if action == "connect" {
		u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	}
	return u.JoinPath(append([]string{"machine", id, action}, tail...)...).String(), nil
}

func enroll(ctx context.Context, state, origin, id, code, mode string) error {
	address, err := endpoint(origin, id, "pair")
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("the enrollment code is required")
	}
	switch mode {
	case modeUser, modeDedicated:
	default:
		return fmt.Errorf("unknown mode %q: expected %s or %s", mode, modeUser, modeDedicated)
	}
	if err := os.MkdirAll(state, 0700); err != nil { //nolint:gosec // The local operator chooses the state directory.
		return err
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile("device.json.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = root.Remove("device.json.tmp") }()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, nil) //nolint:gosec // The operator chooses the validated Dex address.
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+code)
	res, err := http.DefaultClient.Do(req) //nolint:gosec // The operator chooses the validated Dex address.
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return errors.New("the enrollment code is invalid or expired; get a fresh install command")
	case http.StatusConflict:
		return errors.New("this Runner is already enrolled; revoke the machine in Dex first")
	default:
		return fmt.Errorf("the enrollment was refused (%s)", res.Status)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.UnmarshalRead(io.LimitReader(res.Body, 4096), &issued); err != nil || issued.Token == "" {
		return errors.New("the enrollment returned no machine token")
	}
	if err := json.MarshalWrite(file, device{Origin: origin, RunnerID: id, Token: issued.Token, Mode: mode}); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename("device.json.tmp", "device.json"); err != nil {
		return err
	}
	return nil
}

func run(ctx context.Context, state string, noAutoUpdate bool) error {
	if os.Geteuid() == 0 {
		return errors.New("the machine service must not run as root; install it as the account that uses it")
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open("device.json")
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
		return stateError(state, err)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("the machine state must be a private regular file")
	}
	var cfg device
	if err := json.UnmarshalRead(io.LimitReader(file, 1<<16), &cfg); err != nil {
		return errors.New("invalid machine state file")
	}
	if cfg.Token == "" || cfg.RunnerID == "" {
		return errors.New("the machine state is incomplete; run the install command again")
	}
	log.Printf("machine %s: %s", cfg.RunnerID, describeMode(cfg.Mode, currentAccount()))
	address, err := endpoint(cfg.Origin, cfg.RunnerID, "connect")
	if err != nil {
		return err
	}
	workspace, err := filepath.Abs(filepath.Join(state, "workspace"))
	if err != nil {
		return err
	}
	if err := root.MkdirAll("workspace", 0700); err != nil {
		return err
	}
	selfVer := agentVersion()
	exePath, _ := os.Executable()
	capInfo := probeSelfUpdate(selfVer, exePath, noAutoUpdate)
	readyMeta := outbound.Ready{
		Version:          selfVer,
		CanSelfUpdate:    capInfo.Supported,
		SelfUpdateReason: capInfo.Reason,
	}
	header := http.Header{"Authorization": []string{"Bearer " + cfg.Token}}
	execution := runnerExecutionOptions(ctx, workspace)
	delay := time.Second
	var backoff updateBackoff
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		// websocket.Dial takes ownership of the response on a successful upgrade.
		c, res, dialErr := websocket.Dial(attempt, address, &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{outbound.Version}}) //nolint:bodyclose
		cancel()
		// A refused token is terminal, so the agent stops with success and the service manager leaves it stopped.
		if res != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			log.Print("Dex rejected the machine token; install the machine again")
			return nil
		}
		serverVer := ""
		if res != nil {
			serverVer = strings.TrimSpace(res.Header.Get("X-Dex-Version"))
		}
		incompatible := res != nil && res.StatusCode == http.StatusUpgradeRequired
		align := shouldAlignVersion(selfVer, serverVer, capInfo)
		if (dialErr == nil || incompatible) && align && !backoff.blocked(serverVer, time.Now()) {
			if c != nil {
				_ = c.CloseNow()
			}
			log.Printf("aligning dex-agent %s -> %s", selfVer, serverVer)
			if err := selfUpdateTo(ctx, nil, cfg, capInfo.Executable, serverVer); err != nil {
				if errors.Is(err, errUpdateRollback) {
					return err
				}
				log.Printf("auto-update to %s failed: %v", serverVer, err)
				backoff.recordFailure(serverVer, time.Now())
			} else {
				return nil
			}
			// Re-dial immediately (or after backoff) rather than serving on a closed socket.
			if dialErr == nil {
				continue
			}
		}
		if incompatible && !align {
			log.Print("the machine protocol is incompatible; install the matching Dex version")
			return nil
		}
		if dialErr == nil {
			if serverVer == selfVer {
				backoff.clear()
			}
			log.Print("connected to Dex")
			_ = outbound.ServeWithInfo(ctx, c, node.NewNodeExecutionEnv(execution), readyMeta)
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
	return ctx.Err()
}

// runnerExecutionOptions captures the Runner account's PATH once for all connections.
func runnerExecutionOptions(ctx context.Context, workspace string) node.NodeExecutionEnvOptions {
	opts := node.NodeExecutionEnvOptions{Cwd: workspace}
	path, err := loginenv.Path(ctx)
	if err == nil {
		opts.ShellEnv = map[string]string{"PATH": path}
	} else if ctx.Err() == nil {
		log.Printf("login PATH unavailable (%s); tools keep the service PATH", err)
	}
	return opts
}
