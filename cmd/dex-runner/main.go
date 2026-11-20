// dex-runner connects a dedicated execution host to Dex.
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

type device struct {
	Origin      string `json:"origin"`
	Environment string `json:"environmentId"`
	Credential  string `json:"credential"`
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
		return errors.New("usage: dex-runner pair -url https://dex.example -id ENVIRONMENT_ID | dex-runner run")
	}
	data, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	state := flags.String("state", filepath.Join(data, "dex-runner"), "private runner state directory")
	origin, id := new(string), new(string)
	if args[0] == "pair" {
		origin = flags.String("url", "", "Dex HTTPS origin")
		id = flags.String("id", "", "environment ID")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch args[0] {
	case "pair":
		fmt.Fprintln(os.Stderr, "Paste the one-time pairing code on stdin:")
		code, err := bufio.NewReader(io.LimitReader(input, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return pair(ctx, *state, *origin, *id, strings.TrimSpace(code))
	case "run":
		return run(ctx, *state)
	default:
		return errors.New("expected pair or run")
	}
}

func endpoint(origin, id, action, scheme string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("runner requires an HTTPS origin without a path or query")
	}
	if id == "" || strings.ContainsAny(id, "/\\?#.%") {
		return "", errors.New("invalid runner environment ID")
	}
	u.Scheme = scheme
	return u.JoinPath("api", "runner", id, action).String(), nil
}

func pair(ctx context.Context, state, origin, id, code string) error {
	address, err := endpoint(origin, id, "pair", "https")
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("pairing code is required")
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, nil) //nolint:gosec // The operator chooses the validated Dex origin.
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+code)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req) //nolint:gosec // Pair only with the explicitly configured origin; do not follow redirects.
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("pairing refused (%s); check the address and request a fresh code", res.Status)
	}
	var reply struct {
		Credential string `json:"credential"`
	}
	if err := json.UnmarshalRead(io.LimitReader(res.Body, 4096), &reply); err != nil || reply.Credential == "" {
		return errors.New("invalid pairing response; revoke the device and request a fresh code")
	}
	if err := json.MarshalWrite(file, device{Origin: origin, Environment: id, Credential: reply.Credential}); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename("device.json.tmp", "device.json"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Paired. Start the runner with dex-runner run using the same state directory.")
	return nil
}

func run(ctx context.Context, state string) error {
	if os.Geteuid() == 0 {
		return errors.New("run the runner as a dedicated non-root account")
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
		return errors.New("runner credential must be a private regular file")
	}
	var cfg device
	if err := json.UnmarshalRead(io.LimitReader(file, 4096), &cfg); err != nil {
		return errors.New("invalid runner credential file")
	}
	if cfg.Credential == "" {
		return errors.New("missing runner credential")
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
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	delay := time.Second
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		// websocket.Dial takes ownership of the response on a successful upgrade.
		c, res, dialErr := websocket.Dial(attempt, address, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + cfg.Credential}}, Subprotocols: []string{runnerproto.Version}}) //nolint:bodyclose
		cancel()
		if res != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			return errors.New("runner credential rejected; pair this host again")
		}
		if res != nil && res.StatusCode == http.StatusUpgradeRequired {
			return errors.New("runner protocol is incompatible; install the matching Dex runner version")
		}
		if dialErr == nil {
			log.Print("runner connected")
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
