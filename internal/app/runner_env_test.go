package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/runner"
)

// TestUserEnrollsRemoteMachine installs a machine with one command. Dex publishes a pinned TLS
// address, the machine presents its own certificate, and the one-time code enrolls that
// certificate without a second step.
func TestUserEnrollsRemoteMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	a, err := app.Start(ctx, app.Config{DataDir: t.TempDir(), Home: t.TempDir(), FakeModel: true, Token: "test-admin", MachineListen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	d := &dex{app: a, srv: httptest.NewTLSServer(a.Handler())}
	t.Cleanup(d.stop)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	d.srv.Client().Jar = jar
	d.must(t, http.MethodPost, "/api/session", map[string]string{"token": "test-admin"}, nil, http.StatusNoContent)
	var health struct {
		MachineUrls []string `json:"machineUrls"`
		MachinePin  string   `json:"machinePin"`
	}
	d.must(t, http.MethodGet, "/api/health", nil, &health)
	if len(health.MachineUrls) != 1 || health.MachinePin == "" {
		t.Fatalf("health reports machineUrls=%v pin=%q", health.MachineUrls, health.MachinePin)
	}
	origin, err := url.Parse(health.MachineUrls[0])
	if err != nil {
		t.Fatal(err)
	}
	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{"name": "Debian VM", "kind": "runner"}, &created, http.StatusCreated)
	settingsPath := "/api/settings/environments/" + created.ID
	devicePath := "/api/runner/" + created.ID
	var issued struct {
		Code string `json:"code"`
	}
	d.must(t, http.MethodPost, settingsPath+"/runner-pairing", nil, &issued)

	identityPEM, err := runner.NewMachineIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runner.ParseMachineIdentity(identityPEM)
	if err != nil {
		t.Fatal(err)
	}
	client, err := runner.MachineClient(health.MachinePin, identity)
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(code string, client *http.Client) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin.String()+devicePath+"/pair", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+code)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	// Without the machine certificate the code is useless, and Dex cannot be reached without the pin.
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: pinned(t, health.MachinePin)}}
	if got := enroll(issued.Code, anonymous); got != http.StatusUnauthorized {
		t.Fatalf("enrollment without a machine certificate returned %d", got)
	}
	unpinned, err := runner.MachineClient("", identity)
	if err == nil || unpinned != nil {
		t.Fatalf("a missing pin was accepted: %v", err)
	}
	if got := enroll(issued.Code, client); got != http.StatusNoContent {
		t.Fatalf("enrollment returned %d", got)
	}
	if got := enroll(issued.Code, client); got != http.StatusUnauthorized {
		t.Fatalf("a used code returned %d", got)
	}

	dial := func() (*websocket.Conn, error) {
		u := url.URL{Scheme: "wss", Host: origin.Host, Path: devicePath + "/connect"}
		ws, res, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{runner.Version}})
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		return ws, err
	}
	ws, err := dial()
	if err != nil {
		t.Fatalf("connect with the enrolled certificate: %v", err)
	}
	defer func() { _ = ws.CloseNow() }()
	work := t.TempDir()
	stop := make(chan error, 1)
	go func() {
		stop <- runner.Serve(ctx, ws, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work}))
	}()
	var probe apiEnvironmentTestResult
	for !probe.OK {
		d.must(t, http.MethodPost, settingsPath+"/test", nil, &probe)
		if probe.OK {
			break
		}
		select {
		case err := <-stop:
			t.Fatalf("machine stopped early: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.Provider = "faux"
	bot.ModelID = "bash"
	bot.ThinkingLevel = "off"
	bot.EnvironmentID = created.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, &bot)
	chat := d.newSession(t).ID
	if got := taskTurn(t, d, chat, `printf 'machine-ok'`); !strings.Contains(got, "machine-ok") {
		t.Fatalf("remote command returned %q", got)
	}
	d.must(t, http.MethodDelete, settingsPath+"/runner-pairing", nil, nil)
	select {
	case <-stop:
	case <-ctx.Done():
		t.Fatal("revocation did not close the machine")
	}
	if ws, err := dial(); err == nil {
		_ = ws.CloseNow()
		t.Fatal("a revoked certificate reconnected")
	}
}

// pinned returns the TLS configuration of a machine that trusts only the Dex public key. The
// certificate carries no host name, so the pin is the only identity check.
func pinned(t *testing.T, pin string) *tls.Config {
	t.Helper()
	seed, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(seed) != sha256.Size {
		t.Fatalf("pin %q is not a SHA-256 digest", pin)
	}
	return &tls.Config{ //nolint:gosec // The pinned key replaces host name verification.
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("no Dex certificate")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if !bytes.Equal(sum[:], seed) {
				return errors.New("Dex certificate does not match the pin")
			}
			return nil
		},
	}
}
