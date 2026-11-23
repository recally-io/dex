// dex-agent connects a dedicated execution host to Dex.
package main

import (
	"bufio"
	"bytes"
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
	"github.com/recally-io/dex/internal/execenv/runner"
	"github.com/recally-io/dex/internal/loginenv"
)

// device is the enrolled state. Dex identifies this machine by the token that enrollment
// returned, and stores only its digest. Mode, Account, UID, and Home describe how the machine
// runs; they carry no secret, and status reports them.
type device struct {
	Origin      string `json:"origin"`
	Environment string `json:"environmentId"`
	Token       string `json:"token"`
	Mode        string `json:"mode,omitempty"`
	Account     string `json:"account,omitempty"`
	UID         int    `json:"uid,omitempty"`
	Home        string `json:"home,omitempty"`
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
		return errors.New("usage: dex-agent enroll -url https://dex.example.com -id ENVIRONMENT_ID -code CODE [-mode user] | dex-agent run | dex-agent status")
	}
	private, err := defaultState()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	state := flags.String("state", private, "private machine state directory")
	origin, id, code, mode := new(string), new(string), new(string), new(string)
	if args[0] == "enroll" {
		origin = flags.String("url", "", "Dex address, for example https://dex.example.com")
		id = flags.String("id", "", "environment ID")
		code = flags.String("code", "", "one-time enrollment code from the install command")
		*mode = modeDedicated
		flags.StringVar(mode, "mode", modeDedicated, "user: run work as the account that installed this machine; dedicated: run work as a dedicated account")
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
		return run(ctx, *state)
	case "status":
		return status(ctx, *state, os.Stdout)
	default:
		return errors.New("expected enroll, run, or status")
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

func endpoint(origin, id, action string) (string, error) {
	u, err := url.Parse(origin)
	// A loopback address may use plain HTTP, so a developer can test the flow without a proxy.
	secure := u != nil && (u.Scheme == "https" || u.Scheme == "http" && u.Hostname() == "127.0.0.1")
	if err != nil || !secure || u.User != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the machine address must be an HTTPS origin without a path or query")
	}
	if id == "" || strings.ContainsAny(id, "/\\?#.%") {
		return "", errors.New("invalid environment ID")
	}
	if action == "connect" {
		u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	}
	return u.JoinPath("machine", id, action).String(), nil
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
	// A machine that runs as its install user reports that account, so Dex never leaves it open to
	// an unrelated bot: the account holds the home, the login state, and the credentials of a person.
	identity := device{Mode: mode}
	if mode == modeUser {
		a := currentAccount()
		identity.Account, identity.UID, identity.Home = a.Name, a.UID, a.Home
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
	body, err := json.Marshal(map[string]string{"runAsUser": identity.runAsUser()})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body)) //nolint:gosec // The operator chooses the validated Dex address.
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+code)
	req.Header.Set("Content-Type", "application/json")
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
		return errors.New("this environment is already enrolled; revoke the machine in Dex first")
	default:
		return fmt.Errorf("the enrollment was refused (%s)", res.Status)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.UnmarshalRead(io.LimitReader(res.Body, 4096), &issued); err != nil || issued.Token == "" {
		return errors.New("the enrollment returned no machine token")
	}
	enrolled := identity
	enrolled.Origin, enrolled.Environment, enrolled.Token = origin, id, issued.Token
	if err := json.MarshalWrite(file, enrolled); err != nil {
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

func run(ctx context.Context, state string) error {
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
	if cfg.Token == "" {
		return errors.New("the machine state is incomplete; run the install command again")
	}
	// The mode is part of the state, so the log names the account that the work will run as.
	cfg.Mode = modeOf(cfg)
	log.Printf("machine %s runs as %s", cfg.Environment, describeMode(cfg))
	address, err := endpoint(cfg.Origin, cfg.Environment, "connect")
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
	header := http.Header{"Authorization": []string{"Bearer " + cfg.Token}}
	execution := runnerExecutionOptions(ctx, workspace)
	delay := time.Second
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		// websocket.Dial takes ownership of the response on a successful upgrade.
		c, res, dialErr := websocket.Dial(attempt, address, &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{runner.Version}}) //nolint:bodyclose
		cancel()
		// A refused token and an incompatible protocol are terminal, so the agent stops with
		// success and the service manager leaves it stopped.
		if res != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			log.Print("Dex rejected the machine token; install the machine again")
			return nil
		}
		if res != nil && res.StatusCode == http.StatusUpgradeRequired {
			log.Print("the machine protocol is incompatible; install the matching Dex version")
			return nil
		}
		if dialErr == nil {
			log.Print("connected to Dex")
			_ = runner.Serve(ctx, c, node.NewNodeExecutionEnv(execution))
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
