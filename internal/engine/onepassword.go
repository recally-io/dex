package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// OnePassword selects how tools authenticate the 1Password CLI. dex does not read secrets; it
// passes the credentials to tools, and the prompt tells the agent to run op.
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
	lines := []string{".env: values are in the tool environment. Use $KEY in bash and ${KEY} in mcp.json headers. Never print, echo, or write a value into chat, memory, or files. The user sets values on the Environment page and in the Tools tab."}
	if op.Mode != "" && opPath != "" {
		lines = append(lines,
			"1Password: the op CLI is signed in through the tool environment. Read a secret inside the command that needs it, for example curl -H \"Authorization: Bearer $(op read 'op://<vault>/<item>/<field>')\". An environment value that starts with op:// is a reference: use $(op read \"$KEY\"). For a stdio MCP server, run it through op run -- <command>, and put op:// references in its env. Never print a secret.")
	}
	return strings.Join(lines, "\n")
}
