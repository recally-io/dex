package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUserServiceDefinitions(t *testing.T) {
	special := "space & \"quote\" 中文 %h $HOME \\path"
	var tools []string
	for i := range 100 {
		tools = append(tools, fmt.Sprintf("/tools/%d", i))
	}
	for _, auto := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto=%t", auto), func(t *testing.T) {
			c := Config{Host: "127.0.0.1", Port: 18787, DataDir: "/data/" + special, Executable: "/opt/" + special + "/dex", Path: strings.Join(tools, ":") + ":/tools/" + special, AutoUpdate: auto, Channel: "prerelease"}
			mainPlist := plist(c, "/home/"+special, "/logs/"+special+"/dex.log")
			for name, data := range map[string][]byte{"darwin": mainPlist, "linux": unit(c)} {
				restored, err := readConfig(data)
				if err != nil || restored != c {
					t.Fatalf("configuration changed: %+v: %v", restored, err)
				}
				golden := filepath.Join("testdata", fmt.Sprintf("%s-%t.golden", name, auto))
				combined := data
				if auto {
					if name == "darwin" {
						combined = append(bytes.Clone(data), launchPlist(label+".update", []string{c.Executable, "update", "--scheduled"}, c.Path, "/home/"+special, "/logs/dex.log", "", true)...)
					} else {
						combined = append(bytes.Clone(data), updateUnit(c)...)
						combined = append(combined, []byte(updateTimer)...)
					}
				}
				if os.Getenv("DEX_UPDATE_GOLDENS") == "1" {
					if err := os.MkdirAll("testdata", 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, combined, 0600); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(want, combined) {
					t.Fatalf("definition differs from %s", golden)
				}
			}
			decoder := xml.NewDecoder(bytes.NewReader(mainPlist))
			var values []string
			for {
				token, err := decoder.Token()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
					var value string
					if err := decoder.DecodeElement(&value, &start); err != nil {
						t.Fatal(err)
					}
					values = append(values, value)
				}
			}
			for _, value := range append(c.args(), c.Path) {
				if !slices.Contains(values, value) {
					t.Fatalf("plist changed %q", value)
				}
			}
			dir := t.TempDir()
			if runtime.GOOS == "darwin" {
				for name, data := range map[string][]byte{"dex.plist": mainPlist, "update.plist": launchPlist(label+".update", []string{c.Executable, "update", "--scheduled"}, c.Path, "/home/test", "/logs/dex.log", "", true)} {
					file := filepath.Join(dir, name)
					if err := os.WriteFile(file, data, 0600); err != nil {
						t.Fatal(err)
					}
					if b, err := exec.CommandContext(t.Context(), "plutil", "-lint", file).CombinedOutput(); err != nil {
						t.Fatalf("plutil: %s: %v", b, err)
					}
				}
			}
			if runtime.GOOS == "linux" {
				if _, err := exec.LookPath("systemd-analyze"); err != nil {
					t.Skip("systemd-analyze not installed")
				}
				for name, data := range map[string][]byte{"dex.service": unit(c), "dex-update.service": updateUnit(c), "dex-update.timer": []byte(updateTimer)} {
					if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if b, err := exec.CommandContext(t.Context(), "systemd-analyze", "verify", filepath.Join(dir, "dex.service"), filepath.Join(dir, "dex-update.service"), filepath.Join(dir, "dex-update.timer")).CombinedOutput(); err != nil {
					t.Fatalf("systemd-analyze: %s: %v", b, err)
				}
			}
		})
	}
}
func TestUserCannotInstallTemporaryBuild(t *testing.T) {
	_, err := Executable()
	if err == nil || !strings.Contains(err.Error(), "temporary build") {
		t.Fatalf("want temporary build refusal; got %v", err)
	}
}
func TestUserLoopbackAddress(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.9.2.3", "::1", "localhost"} {
		c := Config{Host: host, Port: 18787, DataDir: t.TempDir(), Channel: "stable"}
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if host == "localhost" && c.Host != "127.0.0.1" {
			t.Fatal("localhost was not normalized")
		}
	}
	for _, host := range []string{"", "0.0.0.0", "::", "192.168.0.1", "example.com", "::ffff:127.0.0.1"} {
		c := Config{Host: host, Port: 18787, DataDir: t.TempDir(), Channel: "stable"}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "no authentication") {
			t.Fatalf("host %q: %v", host, err)
		}
	}
}
func TestUserEnvironmentDefaults(t *testing.T) {
	t.Setenv("DEX_ADDR", "")
	t.Setenv("DEX_DATA_DIR", "")
	c, err := Defaults()
	if err != nil || c.Host != "127.0.0.1" || c.Port != 8787 {
		t.Fatalf("%+v: %v", c, err)
	}
	t.Setenv("DEX_ADDR", "[::1]:18787")
	t.Setenv("DEX_DATA_DIR", t.TempDir())
	c, err = Defaults()
	if err != nil || c.Host != "::1" || c.Port != 18787 {
		t.Fatalf("%+v: %v", c, err)
	}
}
func TestUserReadinessChecksIdentityAndWorker(t *testing.T) {
	for _, body := range []string{`{"status":"ok","version":"v1.0.0","dataDir":"/other","workerReady":true}`, `{"status":"ok","version":"v0.9.0","dataDir":"/data","workerReady":true}`, `{"status":"ok","version":"v1.0.0","dataDir":"/data","workerReady":false}`, `{"status":"ok","version":"v1.0.0","dataDir":"/data","workerReady":true}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		t.Cleanup(server.Close)
		c := Config{Host: "127.0.0.1", Port: server.Listener.Addr().(*net.TCPAddr).Port, DataDir: "/data"}
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		err := Ready(ctx, c, "v1.0.0")
		cancel()
		if strings.Contains(body, `"dataDir":"/data","workerReady":true`) && strings.Contains(body, `"version":"v1.0.0"`) {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("readiness accepted a different instance or unavailable worker")
		}
	}
}

func TestUserInvalidConfigurationFailsBeforeDefinitionWrite(t *testing.T) {
	for _, port := range []int{-1, 0, 65536} {
		c := Config{Host: "127.0.0.1", Port: port, DataDir: t.TempDir(), Channel: "stable"}
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted port %d", port)
		}
	}
	for _, path := range []string{string([]byte{255}), "/tools/\x01"} {
		c := Config{Host: "127.0.0.1", Port: 18787, DataDir: t.TempDir(), Channel: "stable", Path: path}
		if err := c.Validate(); err == nil {
			t.Fatal("accepted a path the definition cannot encode")
		}
	}
}
