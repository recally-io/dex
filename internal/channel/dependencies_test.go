package channel_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const channelPath = "github.com/recally-io/dex/internal/channel"
const telegramPolicyPath = channelPath + "/telegram/policy"

func TestChannelImportBoundaries(t *testing.T) {
	const root = channelPath
	const delivery = root + "/delivery"
	packages := listPackages(t, root+"/...")
	var adapters []string
	for _, pkg := range packages {
		name, child := strings.CutPrefix(pkg, root+"/")
		if child && pkg != delivery && !strings.Contains(name, "/") {
			adapters = append(adapters, pkg)
		}
	}
	if len(adapters) == 0 {
		t.Fatalf("go list found no channel adapters under %s", root)
	}
	for _, pkg := range packages {
		t.Run(pkg, func(t *testing.T) {
			for _, dep := range listPackages(t, "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg) {
				if pkg == dep {
					continue
				}
				if pkg == root || (pkg != delivery && within(dep, delivery)) {
					t.Errorf("%s must not depend on %s", pkg, dep)
				}
				for _, adapter := range adapters {
					if within(dep, adapter) && (!within(pkg, adapter) ||
						within(pkg, telegramPolicyPath) != within(dep, telegramPolicyPath)) {
						t.Errorf("%s must not depend on adapter package %s", pkg, dep)
					}
				}
			}
		})
	}
}

func TestOnlyAppImportsTelegramPolicy(t *testing.T) {
	t.Parallel()
	const app = "github.com/recally-io/dex/internal/app"
	// Keep import data available when the embedded web assets are not built.
	const format = `{{$pkg := .ImportPath}}{{range .Imports}}{{printf "%s=%s\n" $pkg .}}{{end}}{{range .TestImports}}{{printf "%s=%s\n" $pkg .}}{{end}}{{range .XTestImports}}{{printf "%s=%s\n" $pkg .}}{{end}}`
	for _, edge := range listPackages(t, "-e", "-f", format, "github.com/recally-io/dex/...") {
		pkg, dep, _ := strings.Cut(edge, "=")
		if within(dep, telegramPolicyPath) && pkg != app && !within(pkg, telegramPolicyPath) {
			t.Errorf("%s imports %s; only %s registers the policy", pkg, dep, app)
		}
	}
}

func TestDeliveryHasNoTelegramRules(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir("delivery", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := strings.ToLower(entry.Name())
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(name, "telegram") || name == "speech.go" || strings.Contains(strings.ToLower(string(source)), "telegram") {
			t.Errorf("%s contains a Telegram rule or reference", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func listPackages(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, output)
	}
	return strings.Fields(string(output))
}

func within(pkg, root string) bool {
	return pkg == root || strings.HasPrefix(pkg, root+"/")
}
