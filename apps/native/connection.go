package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/localdex"
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
	dir := filepath.Join(appDir, "dex")
	if mac {
		dir = os.Getenv("DEX_DATA_DIR")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			dir = filepath.Join(home, ".dex")
		}
	} else if appDir == "" {
		return nil, errors.New("app data directory is not available")
	}
	instance, err := localdex.Start(ctx, localdex.Config{DataDir: dir, Version: version, Mobile: !mac})
	if err != nil {
		return nil, err
	}
	w.instance = instance
	return instance, nil
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

func canonicalOrigin(input string, mac bool) (string, bool) {
	input = strings.TrimSpace(input)
	u, err := url.Parse(input)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") || strings.ContainsAny(input, "?#") ||
		strings.HasSuffix(u.Host, ":") {
		return "", false
	}

	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && !(mac && u.Scheme == "http" && host == "127.0.0.1") {
		return "", false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || ip.Is4In6() || ip.Is6() != strings.HasPrefix(u.Host, "[") {
			return "", false
		}
		host = ip.String()
	} else if strings.HasPrefix(u.Host, "[") || !isDNSHost(host) {
		return "", false
	}

	if raw := u.Port(); raw != "" {
		port, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || port == 0 {
			return "", false
		}
		if !(u.Scheme == "https" && port == 443 || u.Scheme == "http" && port == 80) {
			return u.Scheme + "://" + net.JoinHostPort(host, strconv.FormatUint(port, 10)), true
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, true
}

func isDNSHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	// WebKit can interpret a numeric final label as an IPv4 address.
	_, last, hasDot := strings.CutLast(host, ".")
	if !hasDot {
		last = host
	}
	digits := strings.Trim(last, "0123456789") == ""
	hex := strings.HasPrefix(last, "0x") && strings.Trim(last[2:], "0123456789abcdef") == ""
	return !digits && !hex
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
		return fmt.Errorf("Dex health returned HTTP %d", resp.StatusCode)
	}
}
