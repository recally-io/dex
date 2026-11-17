package execenvtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/env/daemon"
)

// DispatchHelperMain handles --dex-fake-ssh and --dex-pi-env-daemon subprocess invocations from TestMain.
func DispatchHelperMain() {
	if len(os.Args) >= 3 && os.Args[1] == "--dex-fake-ssh" {
		os.Exit(RunFakeSSH(os.Args[2], os.Args[3:]))
	}
	if len(os.Args) >= 5 && os.Args[1] == "--dex-pi-env-daemon" && os.Args[2] == "serve" && os.Args[3] == "--token" {
		if err := daemon.Serve(os.Stdin, os.Stdout, os.Args[4], daemon.Options{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

// FakeSSHFixture holds a simulated SSH server backed by a local shell + host-key file.
type FakeSSHFixture struct {
	StateDir   string
	RemoteHome string
	SSHProgram string
}

// NewFakeSSH sets up a simulated SSH endpoint and pre-seeds the cached pi-env daemon wrapper under dataDir.
func NewFakeSSH(t *testing.T, dataDir string) *FakeSSHFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	remoteHome := filepath.Join(stateDir, "home")
	if err := os.MkdirAll(remoteHome, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &FakeSSHFixture{
		StateDir:   stateDir,
		RemoteHome: remoteHome,
		SSHProgram: filepath.Join(stateDir, "fake-ssh"),
	}
	f.RotateHostKey(t)

	sshScript := fmt.Sprintf("#!/bin/sh\nexec %q --dex-fake-ssh %q \"$@\"\n", exe, stateDir)
	if err := os.WriteFile(f.SSHProgram, []byte(sshScript), 0o700); err != nil { //nolint:gosec // test wrapper script.
		t.Fatal(err)
	}

	// Pre-seed cached daemon wrapper so tests do not shell out to `go build`.
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	cacheBin := filepath.Join(dataDir, "cache", "pi-env", runtime.GOOS+"-"+arch, "pi-env")
	if err := os.MkdirAll(filepath.Dir(cacheBin), 0o700); err != nil {
		t.Fatal(err)
	}
	daemonScript := fmt.Sprintf("#!/bin/sh\nexec %q --dex-pi-env-daemon \"$@\"\n", exe)
	if err := os.WriteFile(cacheBin, []byte(daemonScript), 0o700); err != nil { //nolint:gosec // test wrapper script.
		t.Fatal(err)
	}
	return f
}

// RotateHostKey generates a fresh simulated SSH host public key.
func (f *FakeSSHFixture) RotateHostKey(t *testing.T) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// OpenSSH wire format for ssh-ed25519: string("ssh-ed25519") + string(32-byte key).
	wire := append([]byte{0, 0, 0, 11}, []byte("ssh-ed25519")...)
	wire = append(wire, 0, 0, 0, 32)
	wire = append(wire, pub...)
	line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire) + "\n"
	if err := os.WriteFile(filepath.Join(f.StateDir, "pubkey"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// RunFakeSSH emulates OpenSSH StrictHostKeyChecking (accept-new / yes) and runs the remote command in stateDir/home.
func RunFakeSSH(stateDir string, sshArgs []string) int {
	if len(sshArgs) == 0 {
		return 255
	}
	var strict, knownHosts, alias string
	cmdStart := len(sshArgs) - 1
	for i := 0; i < len(sshArgs); i++ {
		if sshArgs[i] == "--" && i+2 < len(sshArgs) {
			cmdStart = i + 2
			break
		}
		if sshArgs[i] == "-o" && i+1 < len(sshArgs) {
			opt := sshArgs[i+1]
			switch {
			case strings.HasPrefix(opt, "StrictHostKeyChecking="):
				strict = strings.TrimPrefix(opt, "StrictHostKeyChecking=")
			case strings.HasPrefix(opt, "UserKnownHostsFile="):
				knownHosts = strings.Trim(strings.TrimPrefix(opt, "UserKnownHostsFile="), `"`)
			case strings.HasPrefix(opt, "HostKeyAlias="):
				alias = strings.TrimPrefix(opt, "HostKeyAlias=")
			}
			i++
		}
	}

	pubRaw, err := os.ReadFile(filepath.Join(stateDir, "pubkey")) //nolint:gosec // test fixture directory.
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 255
	}
	pubFields := strings.Fields(string(pubRaw))
	keyType, keyData := pubFields[0], pubFields[1]

	switch strict {
	case "accept-new":
		if knownHosts != "" && alias != "" {
			f, openErr := os.OpenFile(knownHosts, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test known_hosts path from PiG.
			if openErr != nil {
				return 255
			}
			_, _ = fmt.Fprintf(f, "%s %s %s\n", alias, keyType, keyData)
			_ = f.Close()
		}
	case "yes":
		knownRaw, readErr := os.ReadFile(knownHosts) //nolint:gosec // test known_hosts path from PiG.
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "Host key verification failed.")
			return 255
		}
		matched, changed := false, false
		for line := range strings.SplitSeq(string(knownRaw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[0] == alias && fields[1] == keyType {
				if fields[2] == keyData {
					matched = true
				} else {
					changed = true
				}
			}
		}
		if changed {
			fmt.Fprintln(os.Stderr, "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @")
			fmt.Fprintln(os.Stderr, "Host key verification failed.")
			return 255
		}
		if !matched {
			fmt.Fprintln(os.Stderr, "No ED25519 host key is known for "+alias+".")
			fmt.Fprintln(os.Stderr, "Host key verification failed.")
			return 255
		}
	}

	remoteHome := filepath.Join(stateDir, "home")
	cmd := exec.CommandContext(context.Background(), "sh", "-c", strings.Join(sshArgs[cmdStart:], " ")) //nolint:gosec // test helper runs command in isolated temp home.
	cmd.Dir = remoteHome
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + remoteHome,
		"SHELL=/bin/sh",
		"REMOTE_SSH_MARKER=ssh-fake-ok",
	}
	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}
