package server_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/server"
)

// installFixture prepares the one install command of a machine without a machine, a service
// manager, root, or a network: the script, an archive with the real service templates, and stub
// commands that record what the installer asks of the operating system.
type installFixture struct {
	root    string
	home    string
	log     string
	payload string
	stubs   string
	environ []string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	root := t.TempDir()
	f := &installFixture{
		root:    root,
		home:    filepath.Join(root, "home"),
		log:     filepath.Join(root, "calls.log"),
		payload: filepath.Join(root, "payload"),
		stubs:   filepath.Join(root, "stubs"),
	}
	for _, dir := range []string{f.home, f.payload, f.stubs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.writeStubs(t, f.stubs)
	f.writePayload(t)
	// The installer the operator downloads, with the tag that Dex serves for this build.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/install-machine.sh", nil)
	res := httptest.NewRecorder()
	server.New(server.Config{Version: "dev"}).ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("download the installer: %d", res.Code)
	}
	script := filepath.Join(root, "install-machine.sh")
	if err := os.WriteFile(script, res.Body.Bytes(), 0o700); err != nil {
		t.Fatal(err)
	}
	f.environ = append(os.Environ(),
		"HOME="+f.home,
		"DEX_TEST_LOG="+f.log,
		"DEX_TEST_PAYLOAD="+f.payload,
		"XDG_STATE_HOME=", "XDG_BIN_HOME=", "XDG_CONFIG_HOME=", "XDG_RUNTIME_DIR=/run/user/501",
	)
	return f
}

// writePayload writes the archive that Dex serves plus its checksums, with the real service
// templates of this build and a stub agent.
func (f *installFixture) writePayload(t *testing.T) {
	t.Helper()
	archive := filepath.Join(f.payload, "archive.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	agent := "#!/bin/sh\nprintf 'dex-agent|%s\\n' \"$*\" >>\"$DEX_TEST_LOG\"\nread -r code || code=\nprintf 'dex-agent-stdin|%s\\n' \"$code\" >>\"$DEX_TEST_LOG\"\nexit 0\n"
	files := map[string]string{
		"dex-agent":                   agent,
		"dex-agent.service":           readTemplate(t, "dex-agent.service"),
		"dex-agent-user.service":      readTemplate(t, "dex-agent-user.service"),
		"com.recally.dex-agent.plist": readTemplate(t, "com.recally.dex-agent.plist"),
	}
	for name, body := range files {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	var checksums strings.Builder
	for _, osName := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			fmt.Fprintf(&checksums, "%s  dex-agent_v0.0.0-dev_%s_%s.tar.gz\n", hex.EncodeToString(sum[:]), osName, arch)
		}
	}
	if err := os.WriteFile(filepath.Join(f.payload, "checksums.txt"), []byte(checksums.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readTemplate returns one of the service files that this build packages.
func readTemplate(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "dex-agent", "packaging", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (f *installFixture) writeStubs(t *testing.T, dir string) {
	t.Helper()
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// curl copies the payload that the Dex server would serve.
	stub("curl", `out=
while [ "$#" -gt 0 ]; do
	case $1 in
		-o) out=$2; shift 2 ;;
		-*) shift ;;
		*) url=$1; shift ;;
	esac
done
case $url in
	*checksums.txt) cp "$DEX_TEST_PAYLOAD/checksums.txt" "$out" ;;
	*.tar.gz) cp "$DEX_TEST_PAYLOAD/archive.tar.gz" "$out" ;;
	*) exit 22 ;;
esac
`)
	stub("uname", `case $1 in
	-s) printf '%s\n' "${FAKE_OS:-Linux}" ;;
	-m) printf '%s\n' "${FAKE_ARCH:-aarch64}" ;;
	*) printf 'unknown\n' ;;
esac
`)
	stub("id", `case $1 in
	-u) printf '%s\n' "${FAKE_UID:-501}" ;;
	*) printf '%s\n' "${FAKE_USER:-vaayne}" ;;
esac
`)
	stub("systemctl", `printf 'systemctl|%s\n' "$*" >>"$DEX_TEST_LOG"
if [ "$1" = list-unit-files ]; then printf '%s' "${FAKE_UNIT_FILES:-}"; fi
exit 0
`)
	// loginctl grants linger when the fixture allows it, and refuses it otherwise.
	stub("loginctl", `printf 'loginctl|%s\n' "$*" >>"$DEX_TEST_LOG"
if [ "$1" = show-user ]; then
	if [ "${FAKE_LINGER:-grant}" = grant ] && [ -e "$DEX_TEST_LOG.linger" ]; then printf 'yes\n'; else printf 'no\n'; fi
fi
if [ "$1" = enable-linger ] && [ "${FAKE_LINGER:-grant}" = grant ]; then : >"$DEX_TEST_LOG.linger"; fi
exit 0
`)
	stub("launchctl", `printf 'launchctl|%s\n' "$*" >>"$DEX_TEST_LOG"
exit 0
`)
	stub("sudo", `if [ "$1" = -u ]; then shift 2; elif [ "$1" = -v ]; then exit 0; fi
exec "$@"
`)
}

// runRootStubs adds the stubs of a system install, which writes to directories that only root
// owns. They record the call instead of touching the host.
func (f *installFixture) runRootStubs(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(f.root, "root-stubs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stub("install", `printf 'install|%s\n' "$*" >>"$DEX_TEST_LOG"
exit 0
`)
	stub("runuser", `printf 'runuser|%s\n' "$*" >>"$DEX_TEST_LOG"
read -r code || code=
printf 'runuser-stdin|%s\n' "$code" >>"$DEX_TEST_LOG"
exit 0
`)
	stub("useradd", `printf 'useradd|%s\n' "$*" >>"$DEX_TEST_LOG"
exit 0
`)
	stub("getent", `if [ "$1" = passwd ] && [ "$2" = dex-agent ]; then exit 0; fi
exit 1
`)
	return dir
}

// run executes the installer with the environment of the fixture.
func (f *installFixture) run(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", append([]string{filepath.Join(f.root, "install-machine.sh")}, args...)...) //nolint:gosec // The fixture runs its own installer.
	cmd.Env = append(append([]string{}, f.environ...), env...)
	path := f.stubs + ":/usr/bin:/bin:/usr/sbin:/sbin"
	for _, item := range env {
		if rest, ok := strings.CutPrefix(item, "PATH="); ok {
			path = rest
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *installFixture) calls(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestUserInstallsTheMachineAsTheAccountThatUsesIt runs the install command of a machine without
// a machine: no root, no service manager, no network, and no real home.
func TestUserInstallsTheMachineAsTheAccountThatUsesIt(t *testing.T) {
	t.Run("an account installs the machine for itself", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t, nil, "https://dex.example.com", "01JRUN", "one-time-code")
		if err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		state := filepath.Join(f.home, ".local", "state", "dex-agent")
		if info, err := os.Stat(state); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("state directory %s (err %v)", state, err)
		}
		if info, err := os.Stat(filepath.Join(f.home, ".local", "bin", "dex-agent")); err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("machine binary (err %v)", err)
		}
		unit := filepath.Join(f.home, ".config", "systemd", "user", "dex-agent.service")
		raw, err := os.ReadFile(unit)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf(`ExecStart="%s" run -state "%s"`, filepath.Join(f.home, ".local", "bin", "dex-agent"), state); !strings.Contains(string(raw), want) {
			t.Fatalf("the user unit does not run the machine of this account, want %q:\n%s", want, raw)
		}
		if !strings.Contains(string(raw), "WantedBy=default.target") || strings.Contains(string(raw), "User=") {
			t.Fatalf("the user unit is not a user unit:\n%s", raw)
		}
		calls := f.calls(t)
		for _, want := range []string{
			"dex-agent|enroll -state " + state + " -url https://dex.example.com -id 01JRUN -mode user",
			"dex-agent-stdin|one-time-code",
			"systemctl|--user enable --now dex-agent.service",
			"loginctl|enable-linger vaayne",
		} {
			if !strings.Contains(calls, want) {
				t.Fatalf("the installer did not run %q:\n%s", want, calls)
			}
		}
		if strings.Contains(out, "loginctl enable-linger") {
			t.Fatalf("a machine that lingers still warns about it:\n%s", out)
		}
	})

	t.Run("a machine that cannot linger says so", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t, []string{"FAKE_LINGER=deny"}, "https://dex.example.com", "01JRUN", "one-time-code")
		if err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if !strings.Contains(out, "sudo loginctl enable-linger vaayne") {
			t.Fatalf("a machine that stops at logout does not say so:\n%s", out)
		}
	})

	t.Run("root installs nothing without a dedicated account", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t, []string{"FAKE_UID=0"}, "https://dex.example.com", "01JRUN", "one-time-code")
		if err == nil {
			t.Fatalf("root installed the machine:\n%s", out)
		}
		if !strings.Contains(out, "without sudo") {
			t.Fatalf("root is not told to use its own account:\n%s", out)
		}
		if calls := f.calls(t); strings.Contains(calls, "enroll") {
			t.Fatalf("a refused install enrolled the machine:\n%s", calls)
		}
	})

	t.Run("an install of a dedicated account is not replaced", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t, []string{"FAKE_UNIT_FILES=dex-agent.service enabled\n"}, "https://dex.example.com", "01JRUN", "one-time-code")
		if err == nil {
			t.Fatalf("the installer replaced the machine of the dedicated account:\n%s", out)
		}
		if !strings.Contains(out, "already runs the machine as the dedicated account") || !strings.Contains(out, "systemctl disable --now dex-agent.service") {
			t.Fatalf("the refusal does not explain the existing machine:\n%s", out)
		}
		if calls := f.calls(t); strings.Contains(calls, "enroll") {
			t.Fatalf("a refused install enrolled the machine:\n%s", calls)
		}
	})

	t.Run("a shared host gets a dedicated account", func(t *testing.T) {
		f := newInstallFixture(t)
		rootStubs := f.runRootStubs(t)
		out, err := f.run(t, []string{"FAKE_UID=0", "PATH=" + rootStubs + ":" + f.stubs + ":/usr/bin:/bin:/usr/sbin:/sbin"}, "https://dex.example.com", "01JRUN", "one-time-code", "--dedicated")
		if err != nil {
			t.Fatalf("install a dedicated account: %v\n%s", err, out)
		}
		calls := f.calls(t)
		for _, want := range []string{
			"runuser|-u dex-agent -- install -d -m 0700 /var/lib/dex-agent/bin",
			"/etc/systemd/system/dex-agent.service",
			"runuser|-u dex-agent -- /var/lib/dex-agent/bin/dex-agent enroll -state /var/lib/dex-agent -url https://dex.example.com -id 01JRUN -mode dedicated",
			"runuser-stdin|one-time-code",
			"systemctl|enable --now dex-agent.service",
		} {
			if !strings.Contains(calls, want) {
				t.Fatalf("the installer did not run %q:\n%s", want, calls)
			}
		}
	})

	t.Run("a Mac installs a launch agent", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t, []string{"FAKE_OS=Darwin", "FAKE_ARCH=arm64"}, "https://dex.example.com", "01JRUN", "one-time-code")
		if err != nil {
			t.Fatalf("install on a Mac: %v\n%s", err, out)
		}
		state := filepath.Join(f.home, "Library", "Application Support", "dex-agent")
		binary := filepath.Join(f.home, ".local", "bin", "dex-agent")
		plist := filepath.Join(f.home, "Library", "LaunchAgents", "com.recally.dex-agent.plist")
		raw, err := os.ReadFile(plist)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "__") || !strings.Contains(string(raw), binary) || !strings.Contains(string(raw), state) {
			t.Fatalf("the launch agent does not name the machine of this account:\n%s", raw)
		}
		calls := f.calls(t)
		for _, want := range []string{
			"dex-agent|enroll -state " + state + " -url https://dex.example.com -id 01JRUN -mode user",
			"launchctl|bootstrap gui/501 " + plist,
		} {
			if !strings.Contains(calls, want) {
				t.Fatalf("the installer did not run %q:\n%s", want, calls)
			}
		}
	})

	t.Run("a Mac does not infer an account from sudo", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t, []string{"FAKE_OS=Darwin", "FAKE_UID=0", "SUDO_USER=vaayne"}, "https://dex.example.com", "01JRUN", "one-time-code", "--dedicated")
		if err == nil || !strings.Contains(out, "without sudo") || f.calls(t) != "" {
			t.Fatalf("root install on macOS: %v\n%s\n%s", err, out, f.calls(t))
		}
	})

	t.Run("an incomplete command installs nothing", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t, nil, "https://dex.example.com", "01JRUN")
		if err == nil || !strings.Contains(out, "copy the complete install command from Dex") {
			t.Fatalf("an incomplete command: %v\n%s", err, out)
		}
		if calls := f.calls(t); calls != "" {
			t.Fatalf("an incomplete command reached the system:\n%s", calls)
		}
	})
}
