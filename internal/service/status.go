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
	Installed  bool   `json:"installed"`
	Running    bool   `json:"running"`
	Healthy    bool   `json:"healthy"`
	PID        int    `json:"pid"`
	Config     Config `json:"config"`
	Version    string `json:"version"`
	Definition string `json:"definition"`
	Logs       string `json:"logs"`
}

func Probe(ctx context.Context, c Config) (Health, error) {
	var h Health
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	//nolint:gosec // G704: Config.Validate limits this readiness probe to loopback.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL()+"/api/health", nil)
	if err != nil {
		return h, err
	}
	//nolint:gosec // G704: user-selected loopback service address, with no redirects.
	resp, err := client.Do(req)
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
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		h, err := Probe(ctx, c)
		if err == nil {
			if h.DataDir != c.DataDir || h.Version != version || !h.WorkerReady {
				err = fmt.Errorf("unexpected health: version=%s dataDir=%s workerReady=%t (want %s, %s)", h.Version, h.DataDir, h.WorkerReady, version, c.DataDir)
			} else {
				return nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness deadline: %w", errors.Join(ctx.Err(), last))
		case <-ticker.C:
		}
	}
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
	macStateKnown := false
	if runtime.GOOS == "darwin" {
		// Details are best effort because launchctl print has no stable schema.
		//nolint:gosec // G204: fixed label in the current numeric user domain.
		b, e := exec.CommandContext(ctx, "launchctl", "print", m.target()).Output()
		if e != nil {
			if exit, ok := errors.AsType[*exec.ExitError](e); !ok || exit.ExitCode() != 113 {
				err = e
			}
		} else {
			s.Running = true
			for line := range strings.SplitSeq(string(b), "\n") {
				key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
				if !ok {
					continue
				}
				switch key {
				case "state":
					macStateKnown = true
					s.Running = value == "running"
				case "pid":
					s.PID, _ = strconv.Atoi(value)
				}
			}
			if s.PID > 0 {
				s.Running = true
				macStateKnown = true
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
	s.Healthy = probeErr == nil && h.WorkerReady && h.DataDir == c.DataDir
	if s.Healthy {
		s.Version = h.Version
	}
	if runtime.GOOS == "darwin" && !macStateKnown {
		s.Running = s.Running && probeErr == nil
	}
	return s, nil
}
