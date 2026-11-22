package channel_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestChannelImportBoundaries(t *testing.T) {
	const root = "github.com/recally-io/dex/internal/channel"
	const delivery = root + "/delivery"
	var adapters []string
	for _, pkg := range listPackages(t, root+"/...") {
		name, child := strings.CutPrefix(pkg, root+"/")
		if child && pkg != delivery && !strings.Contains(name, "/") {
			adapters = append(adapters, pkg)
		}
	}
	packages := append([]string{root, delivery}, adapters...)
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
					if pkg != adapter && within(dep, adapter) {
						t.Errorf("%s must not depend on adapter %s", pkg, dep)
					}
				}
			}
		})
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
