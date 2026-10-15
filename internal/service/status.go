package service

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/settings"
)

// Health identifies the running control and confirms that its worker answers ping.
type Health struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	DataDir     string `json:"dataDir"`
	WorkerReady bool   `json:"workerReady"`
}

// Status has stable JSON names for scripts. Unknown PID is zero.
type Status struct {
	Health     *Health `json:"-"`
	Installed  bool    `json:"installed"`
	Running    bool    `json:"running"`
	Healthy    bool    `json:"healthy"`
	PID        int     `json:"pid"`
	Config     Config  `json:"config"`
	Version    string  `json:"version"`
	Definition string  `json:"definition"`
	Logs       string  `json:"logs"`
}

// LoopbackClient never uses a proxy or follows redirects. Callers set request deadlines.
var LoopbackClient = &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

// Request is an API request to the service at c. A service on a host that is not loopback
// requires its stored token, so the request reads it from the data directory.
func Request(ctx context.Context, c Config, method, path string) (*http.Request, error) {
	//nolint:gosec // G704: the configured local service address.
	req, err := http.NewRequestWithContext(ctx, method, c.URL()+path, nil)
	if err != nil || c.Loopback() {
		return req, err
	}
	token, err := settings.StoredToken(ctx, c.DataDir)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func Probe(ctx context.Context, c Config) (Health, error) {
	var h Health
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := Request(ctx, c, http.MethodGet, "/api/health")
	if err != nil {
		return h, err
	}
	//nolint:gosec // G704: user-selected local service address, with no redirects.
	resp, err := LoopbackClient.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("health returned %s", resp.Status)
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &h); err != nil {
		return h, err
	}
	if h.Status != "ok" || h.Version == "" || h.DataDir == "" {
		return h, errors.New("address does not answer as dex")
	}
	return h, nil
}
func Ready(ctx context.Context, c Config, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return poll(ctx, 200*time.Millisecond, func() (bool, error) {
		h, err := Probe(ctx, c)
		if err == nil && (h.DataDir != c.DataDir || h.Version != version || !h.WorkerReady) {
			err = fmt.Errorf("unexpected health: version=%s dataDir=%s workerReady=%t (want %s, %s)", h.Version, h.DataDir, h.WorkerReady, version, c.DataDir)
		}
		return err == nil, err
	})
}
func (m Manager) Status(ctx context.Context) (Status, error) {
	s := Status{Definition: m.Definition(), Logs: m.LogPath()}
	c, err := m.Read()
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.Installed = true
	s.Config = c
	if runtime.GOOS == "darwin" {
		// Details are best effort because launchctl print has no stable schema.
		//nolint:gosec // G204: fixed label in the current numeric user domain.
		b, e := exec.CommandContext(ctx, "launchctl", "print", m.target()).Output()
		if e != nil {
			if exit, ok := errors.AsType[*exec.ExitError](e); !ok || exit.ExitCode() != 113 {
				err = e
			}
		} else {
			for line := range strings.SplitSeq(string(b), "\n") {
				key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
				if !ok {
					continue
				}
				if key == "pid" {
					s.PID, _ = strconv.Atoi(value)
				}
			}
		}
	} else {
		b, e := exec.CommandContext(ctx, "systemctl", "--user", "show", "dex.service", "-p", "ActiveState", "-p", "MainPID").Output()
		err = e
		for line := range strings.SplitSeq(string(b), "\n") {
			key, value, _ := strings.Cut(line, "=")
			if key == "ActiveState" {
				s.Running = value == "active"
			}
			if key == "MainPID" {
				s.PID, _ = strconv.Atoi(value)
			}
		}
	}
	if err != nil {
		return s, err
	}
	h, probeErr := Probe(ctx, c)
	if probeErr == nil {
		s.Health = &h
	}
	s.Healthy = probeErr == nil && h.WorkerReady && h.DataDir == c.DataDir
	if s.Healthy {
		s.Version = h.Version
	}
	if runtime.GOOS == "darwin" {
		s.Running = s.PID > 0 || probeErr == nil
	}
	return s, nil
}

// BinaryVersion runs the installed or staged dex and reads its version.
func BinaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	//nolint:gosec // G204: execute the user's installed dex, without a shell.
	b, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 || fields[0] != "dex" {
		return "", errors.New("installed executable did not report a dex version")
	}
	return fields[1], nil
}
