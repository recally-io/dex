package channel_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/recally-io/dex"
const channelPath = modulePath + "/internal/channel"
const telegramPolicyPath = channelPath + "/telegram/policy"

func TestChannelImportBoundaries(t *testing.T) {
	const root = channelPath
	const delivery = root + "/delivery"
	const pairing = root + "/pairing"
	packages := listPackages(t, root+"/...")
	var adapters []string
	for _, pkg := range packages {
		name, child := strings.CutPrefix(pkg, root+"/")
		if child && pkg != delivery && pkg != pairing && !strings.Contains(name, "/") {
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
				if pkg == root || (pkg != delivery && within(dep, delivery)) || (pkg != pairing && within(dep, pairing)) {
					t.Errorf("%s must not depend on %s", pkg, dep)
				}
				for _, adapter := range adapters {
					if pkg == telegramPolicyPath && dep == root+"/telegram" {
						continue // Policy returns the transport's sealed Album type.
					}
					if pkg == pairing && dep == adapter {
						continue
					}
					if within(dep, adapter) && (!within(pkg, adapter) ||
						within(pkg, telegramPolicyPath) != within(dep, telegramPolicyPath)) {
						t.Errorf("%s must not depend on adapter package %s", pkg, dep)
					}
				}
			}
		})
	}
}

func TestBotAndSessionDoNotDependOnChannels(t *testing.T) {
	t.Parallel()
	for _, pkg := range []string{"bot", "session"} {
		for _, dep := range listPackages(t, "-deps", modulePath+"/internal/"+pkg+"/...") {
			if within(dep, channelPath) {
				t.Errorf("internal/%s depends on %s; the app must wire channels", pkg, dep)
			}
		}
	}
}

var platforms = []string{"telegram", "feishu", "wechat"}

func TestPlatformImportBoundaries(t *testing.T) {
	t.Parallel()
	const app = modulePath + "/internal/app"
	const server = modulePath + "/internal/server"
	const api = modulePath + "/internal/api"
	checkGeneratedAPIImports(t)
	// Keep import data available when the embedded web assets are not built.
	const format = `{{$pkg := .ImportPath}}{{range .Imports}}{{printf "%s=%s\n" $pkg .}}{{end}}{{range .TestImports}}{{printf "%s=%s:test\n" $pkg .}}{{end}}{{range .XTestImports}}{{printf "%s=%s:test\n" $pkg .}}{{end}}`
	for _, edge := range listPackages(t, "-e", "-f", format, modulePath+"/...") {
		pkg, dependency, _ := strings.Cut(edge, "=")
		dep, testOnly := strings.CutSuffix(dependency, ":test")
		if within(dep, telegramPolicyPath) && pkg != app && !within(pkg, telegramPolicyPath) {
			t.Errorf("%s imports %s; only %s registers the policy", pkg, dep, app)
		}
		if pkg == app || pkg == server || pkg == api {
			continue
		}
		for _, platform := range platforms {
			adapter := channelPath + "/" + platform
			if !within(dep, adapter) {
				continue
			}
			if !within(pkg, channelPath) {
				t.Errorf("%s imports %s; only app, server and generated api may import platform adapters here", pkg, dep)
			} else if testOnly && pkg != channelPath+"/pairing" && !within(pkg, adapter) {
				// TestChannelImportBoundaries already checks the production closure.
				t.Errorf("%s tests import %s; tests must follow the platform dependency direction", pkg, dep)
			}
		}
	}
}

func checkGeneratedAPIImports(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("../api/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if ast.IsGenerated(file) {
			continue
		}
		for _, spec := range file.Imports {
			dep, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, platform := range platforms {
				if within(dep, channelPath+"/"+platform) {
					t.Errorf("%s imports %s; only generated API code may import platform adapters", path, dep)
				}
			}
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

func TestBotAndSessionHaveNoPlatformRules(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"../bot", "../session"} {
		if err := checkPlatformRules(t, root); err != nil {
			t.Fatal(err)
		}
	}
}

func checkPlatformRules(t *testing.T, root string) error {
	t.Helper()
	files := token.NewFileSet()
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() && name == "testdata" {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		// Parse all files, including those with build tags.
		file, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for node := range ast.Preorder(file) {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				name := strings.ToLower(n.Sel.Name)
				for _, platform := range platforms {
					if strings.Contains(name, platform) {
						t.Errorf("%s: %s belongs to platform policy", files.Position(n.Sel.Pos()), n.Sel.Name)
					}
				}
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(n.Value)
				if err != nil {
					return err
				}
				for _, platform := range platforms {
					if strings.HasPrefix(value, platform+":") {
						t.Errorf("%s: peer prefix %q belongs to platform policy", files.Position(n.Pos()), platform+":")
					}
				}
			}
		}
		return nil
	})
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
