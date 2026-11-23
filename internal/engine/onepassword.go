package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OnePassword selects how tools and explicit provider references authenticate the CLI.
type OnePassword struct {
	// Mode is "", "connect", or "service-account".
	Mode                string
	ConnectHost         string
	ConnectToken        string
	ServiceAccountToken string
}

// apply sets the credentials of the mode and removes the other mode's variables, because op
// prefers Connect when both are present.
func (o OnePassword) apply(env map[string]string) {
	switch o.Mode {
	case "connect":
		env["OP_CONNECT_HOST"], env["OP_CONNECT_TOKEN"] = o.ConnectHost, o.ConnectToken
		delete(env, "OP_SERVICE_ACCOUNT_TOKEN")
	case "service-account":
		env["OP_SERVICE_ACCOUNT_TOKEN"] = o.ServiceAccountToken
		delete(env, "OP_CONNECT_HOST")
		delete(env, "OP_CONNECT_TOKEN")
	}
}

// findOp returns the path of the op executable in the PATH of env, or "".
func findOp(env map[string]string) string {
	for _, dir := range filepath.SplitList(env["PATH"]) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, "op")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

// secretsGuide tells the agent where secrets come from.
func secretsGuide(op OnePassword, opPath string) string {
	if op.Mode == "" || opPath == "" {
		return ""
	}
	return "1Password: the op CLI is signed in through the tool environment. Read a secret inside the command that needs it, for example curl -H \"Authorization: Bearer $(op read 'op://<vault>/<item>/<field>')\". An environment value that starts with op:// is a reference: use $(op read \"$KEY\"). For a stdio MCP server, run it through op run -- <command>, and put op:// references in its env. Never print a secret."
}

// ResolveProviderAPIKey keeps an explicit 1Password reference out of HTTP headers and storage.
func (a *Agents) ResolveProviderAPIKey(ctx context.Context, key string) (string, error) {
	if !strings.HasPrefix(key, "op://") {
		return key, nil
	}
	env, op := a.env.load(ctx)
	op.apply(env)
	executable := findOp(env)
	if executable == "" {
		return "", errors.New("provider credential reference needs the configured 1Password CLI")
	}
	command := exec.CommandContext(ctx, executable, "read", "--no-newline", key) //nolint:gosec // The configured CLI receives a reference, not a shell command.
	command.Env = make([]string, 0, len(env))
	for name, value := range env {
		command.Env = append(command.Env, name+"="+value)
	}
	command.WaitDelay = time.Second
	body, err := command.Output()
	if err != nil {
		return "", errors.New("provider credential reference could not be read")
	}
	secret := strings.TrimSpace(string(body))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New("provider credential reference did not return one API key")
	}
	return secret, nil
}
