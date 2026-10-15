package service

import (
	"cmp"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Config is stored inside the service definition. Arguments use the same values.
type Config struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	DataDir    string `json:"dataDir"`
	Executable string `json:"executable"`
	Path       string `json:"path"`
	AutoUpdate bool   `json:"autoUpdate"`
	Channel    string `json:"channel"`
}

// Defaults resolves the environment before command flags override its parts.
func Defaults() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	c := Config{Host: "127.0.0.1", Port: 8787, DataDir: cmp.Or(os.Getenv("DEX_DATA_DIR"), filepath.Join(home, ".dex")), Channel: "stable"}
	if addr := os.Getenv("DEX_ADDR"); addr != "" {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return c, fmt.Errorf("DEX_ADDR: %w", err)
		}
		c.Host = host
		c.Port, err = strconv.Atoi(port)
		if err != nil {
			return c, fmt.Errorf("DEX_ADDR port: %w", err)
		}
	}
	return c, nil
}

// Validate requires an IP address host. A host that is not loopback makes the API require the
// access token.
func (c *Config) Validate() error {
	for _, value := range []string{c.DataDir, c.Executable, c.Path} {
		if !utf8.ValidString(value) {
			return errors.New("paths and PATH must use valid UTF-8")
		}
		for _, r := range value {
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xfffe || r == 0xffff {
				return errors.New("paths and PATH must not contain control characters")
			}
		}
	}
	if c.Host == "localhost" {
		c.Host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(c.Host)
	if err != nil || ip.Zone() != "" {
		return errors.New("host must be an IP address, such as 127.0.0.1, a Tailscale address, or 0.0.0.0")
	}
	c.Host = ip.String()
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if c.DataDir == "" {
		return errors.New("data directory must not be empty")
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return err
	}
	if c.Channel != "stable" && c.Channel != "prerelease" {
		return errors.New("channel must be stable or prerelease")
	}
	return nil
}

// Loopback reports whether only this machine can reach the host. The empty Config of a missing
// service has no host and is not loopback.
func (c Config) Loopback() bool {
	ip, err := netip.ParseAddr(c.Host)
	return err == nil && ip.IsLoopback()
}

func (c Config) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// URL is the address that a local client uses. A wildcard host accepts loopback connections.
func (c Config) URL() string {
	host := c.Host
	switch ip, _ := netip.ParseAddr(host); {
	case ip.IsUnspecified() && ip.Is4():
		host = "127.0.0.1"
	case ip.IsUnspecified():
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(c.Port))
}

// args select the stored token, so a DEX_TOKEN that the service manager passes on has no effect
// and local clients always know the token.
func (c Config) args() []string {
	return []string{c.Executable, "serve", "--host", c.Host, "--port", strconv.Itoa(c.Port), "--data-dir", c.DataDir, "--stored-token"}
}
func (c Config) record() string { // Validate guarantees that all strings can be encoded.
	b, _ := json.Marshal(c)
	return base64.StdEncoding.EncodeToString(b)
}

func readConfig(data []byte) (Config, error) {
	var c Config
	_, record, ok := strings.Cut(string(data), "dex-config: ")
	if !ok {
		return c, errors.New("service definition has no dex configuration; uninstall it before reinstalling")
	}
	record, _, _ = strings.Cut(record, "\n")
	record = strings.TrimSuffix(record, " -->")
	b, err := base64.StdEncoding.DecodeString(record)
	if err != nil {
		return c, fmt.Errorf("service configuration: %w", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("service configuration: %w", err)
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	if !filepath.IsAbs(c.Executable) {
		return c, errors.New("installed executable path must be absolute")
	}
	return c, nil
}
