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
	"github.com/recally-io/dex/internal/execenv/runnerproto"
)

// device is the enrolled state. Dex identifies this machine by the certificate key, so no
// bearer credential is stored.
type device struct {
	Origin      string `json:"origin"`
	Environment string `json:"environmentId"`
	ServerPin   string `json:"serverPin"`
	Identity    string `json:"identity"`
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
		return errors.New("usage: dex-agent enroll -url https://dex:8788 -id ENVIRONMENT_ID -code CODE | dex-agent run")
	}
	data, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	state := flags.String("state", filepath.Join(data, "dex-agent"), "private machine state directory")
	origin, id := new(string), new(string)
	pin, code := new(string), new(string)
	if args[0] == "enroll" {
		origin = flags.String("url", "", "Dex machine address, for example https://192.0.2.10:8788")
		id = flags.String("id", "", "environment ID")
		pin = flags.String("server-pin", "", "Dex machine public key pin from the install command")
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
		return enroll(ctx, *state, *origin, *id, *pin, *code)
	case "run":
		return run(ctx, *state)
	default:
		return errors.New("expected enroll or run")
	}
}

func endpoint(origin, id, action, scheme string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the machine address must be an HTTPS origin without a path or query")
	}
	if id == "" || strings.ContainsAny(id, "/\\?#.%") {
		return "", errors.New("invalid environment ID")
	}
	u.Scheme = scheme
	return u.JoinPath("api", "runner", id, action).String(), nil
}

func enroll(ctx context.Context, state, origin, id, pin, code string) error {
	address, err := endpoint(origin, id, "pair", "https")
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("the enrollment code is required")
	}
	identityPEM, err := runner.NewMachineIdentity()
	if err != nil {
		return err
	}
	identity, err := runner.ParseMachineIdentity(identityPEM)
	if err != nil {
		return err
	}
	client, err := runner.MachineClient(pin, identity)
	if err != nil {
		return err
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
	res, err := client.Do(req) //nolint:gosec // The operator choses the validated Dex address from the install command.
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch res.StatusCode {
	case http.StatusNoContent, http.StatusOK:
	case http.StatusUnauthorized:
		return errors.New("the enrollment code is invalid or expired; get a fresh install command")
	case http.StatusConflict:
		return errors.New("this environment is already enrolled; revoke the machine in Dex first")
	default:
		return fmt.Errorf("the enrollment was refused (%s)", res.Status)
	}
	if err := json.MarshalWrite(file, device{Origin: origin, Environment: id, ServerPin: pin, Identity: identityPEM}); err != nil {
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
	if cfg.Identity == "" || cfg.ServerPin == "" {
		return errors.New("the machine state is incomplete; run the install command again")
	}
	address, err := endpoint(cfg.Origin, cfg.Environment, "connect", "wss")
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
	identity, err := runner.ParseMachineIdentity(cfg.Identity)
	if err != nil {
		return err
	}
	client, err := runner.MachineClient(cfg.ServerPin, identity)
	if err != nil {
		return err
	}
	delay := time.Second
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		// websocket.Dial takes ownership of the response on a successful upgrade.
		c, res, dialErr := websocket.Dial(attempt, address, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{runnerproto.Version}}) //nolint:bodyclose
		cancel()
		if res != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			return errors.New("the machine certificate was rejected; install the machine again")
		}
		if res != nil && res.StatusCode == http.StatusUpgradeRequired {
			return errors.New("the machine protocol is incompatible; install the matching Dex version")
		}
		if dialErr == nil {
			log.Print("connected to Dex")
			err = runner.Serve(ctx, c, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: workspace}))
			if errors.Is(err, runnerproto.ErrVersion) {
				return err
			}
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
