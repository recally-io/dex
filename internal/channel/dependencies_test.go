package channel_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestChannelImportBoundaries(t *testing.T) {
	const root = "github.com/recally-io/dex/internal/channel"
	const delivery = root + "/delivery"
	adapters := []string{root + "/telegram", root + "/feishu", "github.com/recally-io/dex/internal/channel/wechat"}
	packages := append([]string{root, delivery}, adapters...)
	for _, pkg := range packages {
		t.Run(pkg, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go list: %v\n%s", err, output)
			}
			for dep := range strings.FieldsSeq(string(output)) {
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

func within(pkg, root string) bool {
	return pkg == root || strings.HasPrefix(pkg, root+"/")
}
