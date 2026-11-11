package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// Shape hints describe a .env value without revealing it. The Extensions API and the system
// prompt carry the hint names only; no hint repeats any part of a value.
const (
	hintEmpty       = "empty"
	hintAssignment  = "assignment"
	hintWhitespace  = "whitespace"
	hintOpReference = "op-reference"
)

// A value that holds a pasted line has NAME= at its start or after whitespace, followed by more
// text. NAME is upper case, like the variables people paste (ANTHROPIC_BASE_URL); this skips a
// libpq DSN (host=a port=1) and Server=a;Database=b, at the cost of a lowercase name such as
// http_proxy. The text after = must not start with =, so a base64 padding such as dGVzdA== is
// not an assignment. A single trailing = (dGVzdDE=) has no text after it. A query string (?a=b)
// has no whitespace before the name.
var assignmentValue = regexp.MustCompile(`(?:^|\s)[A-Z_][A-Z0-9_]*=[^=\s]`)

// envHints returns the shape hints of one value. The result is never nil.
func envHints(value string) []string {
	hints := []string{}
	if value == "" || strings.TrimSpace(value) == "" {
		hints = append(hints, hintEmpty)
	}
	if strings.TrimSpace(value) != value {
		hints = append(hints, hintWhitespace)
	}
	if assignmentValue.MatchString(value) {
		hints = append(hints, hintAssignment)
	}
	if strings.HasPrefix(strings.TrimSpace(value), "op://") {
		hints = append(hints, hintOpReference)
	}
	return hints
}

// hintPrompts has the problems only. An op:// reference is valid (secretsGuide explains it), so
// it stays in the API and the UI but not in the prompt.
var hintPrompts = map[string]string{
	hintEmpty:      "is empty",
	hintAssignment: "looks like an assignment (NAME=...), so the user may have pasted a whole line",
	hintWhitespace: "has leading or trailing whitespace",
}

// maxHintedKeys limits the prompt; a long list of hints would cost tokens on every request.
const maxHintedKeys = 20

// envScopeGuide tells the agent where .env values are visible, and lists the keys with a shape
// hint. Shadowed keys are skipped because their values are not in use.
func envScopeGuide(keys []EnvKeyInfo, opPath string) string {
	lines := []string{
		"Scope: .env values and the 1Password credentials exist only in tools that dex starts (bash and stdio MCP servers). A program that you start outside dex gets none of them. Examples are a terminal app, a GUI app, and another session. A terminal app starts from the user's login shell, so it has only what the shell rc files export.",
		`To cross that boundary, pass the value in the command for the target, for example env KEY="$KEY" cmd. Never print the value.`,
	}
	if opPath != "" {
		lines = append(lines, "When op is available in the target, run op read there, so that the target resolves the reference itself.")
	}
	var hinted []string
	for _, key := range keys {
		if key.Shadowed {
			continue
		}
		var phrases []string
		for _, hint := range key.Hints {
			if phrase, ok := hintPrompts[hint]; ok {
				phrases = append(phrases, phrase)
			}
		}
		if len(phrases) > 0 {
			hinted = append(hinted, fmt.Sprintf("- %s: the value %s", key.Key, strings.Join(phrases, "; ")))
		}
	}
	if len(hinted) > 0 {
		lines = append(lines, "These .env keys have a value with a suspicious shape. The check does not reveal values. If a tool fails to authenticate, tell the user to fix the key:")
		lines = append(lines, hinted[:min(len(hinted), maxHintedKeys)]...)
		if len(hinted) > maxHintedKeys {
			lines = append(lines, fmt.Sprintf("- and %d more keys", len(hinted)-maxHintedKeys))
		}
	}
	return strings.Join(lines, "\n")
}
