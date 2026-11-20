// dex-agent connects a dedicated execution host to Dex.
package main

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/execenv/runner"
)

// device is the enrolled state. Dex identifies this machine by the token that enrollment
// returned, and stores only its digest.
type device struct {
	Origin      string `json:"origin"`
	Environment string `json:"environmentId"`
	Token       string `json:"token"`
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
		return errors.New("usage: dex-agent enroll -url https://dex.example.com -id ENVIRONMENT_ID -code CODE | dex-agent run")
	}
	data, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	state := flags.String("state", filepath.Join(data, "dex-agent"), "private machine state directory")
	origin, id, code := new(string), new(string), new(string)
	if args[0] == "enroll" {
		origin = flags.String("url", "", "Dex address, for example https://dex.example.com")
		id = flags.String("id", "", "environment ID")
		code = flags.String("code", "", "one-time enrollment code from the install command")
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
		return enroll(ctx, *state, *origin, *id, *code)
	case "run":
		return run(ctx, *state)
	default:
		return errors.New("expected enroll or run")
	}
}

// endpoint builds one machine route. A session uses the WebSocket scheme of the origin.
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

func enroll(ctx context.Context, state, origin, id, code string) error {
	address, err := endpoint(origin, id, "pair")
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("the enrollment code is required")
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
	if err := json.MarshalWrite(file, device{Origin: origin, Environment: id, Token: issued.Token}); err != nil {
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
		return errors.New("run the machine service as a dedicated non-root account")
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open("device.json")
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
			_ = runner.Serve(ctx, c, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: workspace}))
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
