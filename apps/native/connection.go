package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/localdex"
)

const macDefaultOrigin = "http://127.0.0.1:8787"

// The lock also lets root cleanup wait for a startup that has not yet posted
// its result to the UI. All other starts and stops run off the UI goroutine.
type localWorkspace struct {
	mu       sync.Mutex
	instance *localdex.Instance
}

func (w *localWorkspace) start(ctx context.Context, appDir, version string, mac bool) (*localdex.Instance, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.instance != nil {
		return w.instance, nil
	}
	dir, err := localDataDir(appDir, mac)
	if err != nil {
		return nil, err
	}
	instance, err := localdex.Start(ctx, localdex.Config{DataDir: dir, Version: version, Mobile: !mac})
	if err != nil {
		return nil, err
	}
	w.instance = instance
	return instance, nil
}

func localDataDir(appDir string, mac bool) (string, error) {
	if mac {
		if dir := os.Getenv("DEX_DATA_DIR"); dir != "" {
			return dir, nil
		}
	}
	if appDir == "" {
		return "", errors.New("app data directory is not available")
	}
	return filepath.Join(appDir, "dex"), nil
}

func (w *localWorkspace) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.instance == nil {
		return nil
	}
	instance := w.instance
	w.instance = nil
	return instance.Close()
}

var errDexAuthRequired = errors.New("remote Dex authentication is required")

func probeDex(ctx context.Context, origin string, mac bool, transport http.RoundTripper) error {
	client := http.Client{
		Transport: transport,
		Timeout:   4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil
	case http.StatusOK:
		if mac && req.URL.Hostname() == "127.0.0.1" {
			return nil
		}
		return errDexAuthRequired
	default:
		return fmt.Errorf("health check for Dex returned HTTP %d", resp.StatusCode)
	}
}
