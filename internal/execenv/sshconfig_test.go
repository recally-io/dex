package execenv_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/recally-io/dex/internal/execenv"
)

func TestUserDiscoversSSHConfigHostsAndIdentityFiles(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	confD := filepath.Join(sshDir, "config.d")
	orbDir := filepath.Join(home, ".orbstack", "ssh")
	for _, dir := range []string{confD, orbDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	mainConfig := `
# Global defaults and includes
Include config.d/*.conf
Include ~/.orbstack/ssh/config

Host devbox
  HostName 10.0.0.42
  User devuser
  Port 2222
  IdentityFile ~/.ssh/id_ed25519

# Duplicate alias should keep first matching values (OpenSSH first-match-wins)
Host devbox
  User ignored-late-user
  Port 9999

Host jump-app
  HostName app.internal
  User deploy
  ProxyJump bastion

Host *.internal
  User wildcard-user

Host !bad
  HostName 1.2.3.4

Match host special exec "true"
  User match-user

Host github.com gitlab.com bitbucket.org codeberg.org
  User git

Host *
  User globaluser
  ServerAliveInterval 60
`
	includeConfig := `
Host build-arm
  HostName 192.168.1.80
`
	orbConfig := `
Host orb-ubuntu
  HostName ubuntu@orb
  Port 32222
`
	for p, body := range map[string]string{
		filepath.Join(sshDir, "config"):          mainConfig,
		filepath.Join(confD, "01-work.conf"):     includeConfig,
		filepath.Join(orbDir, "config"):          orbConfig,
		filepath.Join(sshDir, "id_ed25519"):      "PRIVATE KEY MATERIAL",
		filepath.Join(sshDir, "id_ed25519.pub"):  "ssh-ed25519 AAAA",
		filepath.Join(sshDir, "work_rsa"):        "PRIVATE KEY MATERIAL",
		filepath.Join(sshDir, "known_hosts"):     "example.com ssh-ed25519 AAAA",
		filepath.Join(sshDir, "known_hosts.old"): "old",
		filepath.Join(sshDir, "authorized_keys"): "ssh-ed25519 AAAA",
		filepath.Join(sshDir, ".hidden_key"):     "hidden",
	} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := execenv.DiscoverSSHConfig(home)
	if err != nil {
		t.Fatal(err)
	}

	var aliases []string
	byAlias := map[string]execenv.SSHHostEntry{}
	for _, h := range got.Hosts {
		aliases = append(aliases, h.Alias)
		byAlias[h.Alias] = h
	}
	wantAliases := []string{"build-arm", "orb-ubuntu", "devbox", "jump-app"}
	if !slices.Equal(aliases, wantAliases) {
		t.Fatalf("aliases=%v want=%v", aliases, wantAliases)
	}

	devbox := byAlias["devbox"]
	if devbox.HostName != "10.0.0.42" || devbox.User != "devuser" || devbox.Port != 2222 || devbox.IdentityFile != "~/.ssh/id_ed25519" {
		t.Fatalf("unexpected devbox entry: %+v", devbox)
	}
	jumpApp := byAlias["jump-app"]
	if jumpApp.HostName != "app.internal" || jumpApp.User != "deploy" || jumpApp.ProxyJump != "bastion" {
		t.Fatalf("unexpected jump-app entry: %+v", jumpApp)
	}
	buildArm := byAlias["build-arm"]
	if buildArm.HostName != "192.168.1.80" || buildArm.User != "globaluser" {
		t.Fatalf("unexpected build-arm entry: %+v", buildArm)
	}
	orb := byAlias["orb-ubuntu"]
	if orb.Port != 32222 {
		t.Fatalf("unexpected orb-ubuntu entry: %+v", orb)
	}

	wantKeys := []string{"~/.ssh/id_ed25519", "~/.ssh/work_rsa"}
	if !slices.Equal(got.IdentityFiles, wantKeys) {
		t.Fatalf("identityFiles=%v want=%v", got.IdentityFiles, wantKeys)
	}
}

func TestDiscoverSSHConfigMissingDirectoryReturnsEmpty(t *testing.T) {
	t.Parallel()
	got, err := execenv.DiscoverSSHConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != 0 || len(got.IdentityFiles) != 0 {
		t.Fatalf("expected empty discovery for missing .ssh, got %+v", got)
	}
}
