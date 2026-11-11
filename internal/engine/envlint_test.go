package engine

import (
	"slices"
	"testing"
)

func TestUserEnvValueShapesGetOnlyTheRightHints(t *testing.T) {
	t.Parallel()
	// Given values with and without a bad shape, When dex checks them, Then only real
	// problems get a hint.
	for _, c := range []struct {
		value string
		want  []string
	}{
		{"ANTHROPIC_BASE_URL=https://proxy.example CLAUDE_CODE_OAUTH_TOKEN=sk-fake", []string{hintAssignment}},
		{"FOO=bar", []string{hintAssignment}},
		{"export FOO=bar", []string{hintAssignment}},
		{"HTTP_PROXY=http://x", []string{hintAssignment}},
		{"host=localhost port=5432 dbname=x", nil},
		{"Server=a;Database=b", nil},
		{"dGVzdA==", nil},
		{"dGVzdDE=", nil},
		{"https://host.example/path?a=b&c=d", nil},
		{"-Xmx1g -Dfoo=bar", nil},
		{"plain-token", nil},
		{"", []string{hintEmpty}},
		{"  ", []string{hintEmpty, hintWhitespace}},
		{" token ", []string{hintWhitespace}},
		{"op://vault/item/field", []string{hintOpReference}},
	} {
		got := envHints(c.value)
		if !slices.Equal(got, c.want) {
			t.Errorf("hints of a value with %d bytes = %v, want %v", len(c.value), got, c.want)
		}
	}
}
