package runnertest

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
	"strings"
	"testing"
	"time"
)

// DispatchHelperMain handles --dex-fake-ssh subprocess invocations from TestMain.
func DispatchHelperMain() {
	if len(os.Args) >= 3 && os.Args[1] == "--dex-fake-ssh" {
		os.Exit(runFakeSSH(os.Args[2], os.Args[3:]))
	}
}

// FakeSSHFixture holds a simulated SSH server backed by a local shell + host-key file.
type FakeSSHFixture struct {
	stateDir   string
	RemoteHome string
	SSHProgram string
}

// NewFakeSSH sets up a simulated SSH endpoint backed by the current test executable.
func NewFakeSSH(t *testing.T) *FakeSSHFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	remoteHome := filepath.Join(stateDir, "home")
	allowSkillMirrorCleanup(t, remoteHome)
	if err := os.MkdirAll(remoteHome, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &FakeSSHFixture{
		stateDir:   stateDir,
		RemoteHome: remoteHome,
		SSHProgram: filepath.Join(stateDir, "fake-ssh"),
	}
	f.RotateHostKey(t)

	sshScript := fmt.Sprintf("#!/bin/sh\nexec %q --dex-fake-ssh %q \"$@\"\n", exe, stateDir)
	if err := os.WriteFile(f.SSHProgram, []byte(sshScript), 0o700); err != nil { //nolint:gosec // test wrapper script.
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
	if err := os.WriteFile(filepath.Join(f.stateDir, "pubkey"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Invocations returns how many times dex ran the fake ssh program.
func (f *FakeSSHFixture) Invocations(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.stateDir, "invocations"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

// Hold makes each later ssh run wait until Resume, so a test can act while a dial is in progress.
func (f *FakeSSHFixture) Hold(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.stateDir, "hold"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Resume lets held ssh runs continue.
func (f *FakeSSHFixture) Resume(t *testing.T) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.stateDir, "hold")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// Refuse makes each later ssh run to host fail as a host that is down does.
func (f *FakeSSHFixture) Refuse(t *testing.T, host string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.stateDir, "refuse-"+host), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Allow makes later ssh runs to a previously refused host dial again.
func (f *FakeSSHFixture) Allow(t *testing.T, host string) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.stateDir, "refuse-"+host)); err != nil {
		t.Fatal(err)
	}
}

func runFakeSSH(stateDir string, sshArgs []string) int {
	if len(sshArgs) == 0 {
		return 255
	}
	for _, arg := range sshArgs {
		host := arg[strings.LastIndex(arg, "@")+1:]
		if host == "" || strings.ContainsAny(host, "/ ") {
			continue
		}
		if _, err := os.Stat(filepath.Join(stateDir, "refuse-"+host)); err == nil { //nolint:gosec // test fixture directory.
			fmt.Fprintf(os.Stderr, "ssh: connect to host %s port 22: Connection refused\n", host)
			return 255
		}
	}
	// One byte per run: O_APPEND writes are atomic, so concurrent runs count correctly.
	if f, err := os.OpenFile(filepath.Join(stateDir, "invocations"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil { //nolint:gosec // test fixture directory.
		_, _ = f.Write([]byte{'.'})
		_ = f.Close()
	}
	for {
		if _, err := os.Stat(filepath.Join(stateDir, "hold")); err != nil { //nolint:gosec // test fixture directory.
			break
		}
		time.Sleep(10 * time.Millisecond)
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
