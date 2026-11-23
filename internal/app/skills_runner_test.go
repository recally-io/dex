package app_test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserManagesHostSkillsFromTwoRunners(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	// Given a connected runner and a chat of the default bot.
	start := func(name string) (*machine, int64) {
		t.Helper()
		m := addMachine(t, d, name)
		status, token := m.pair(d, m.issue(d))
		if status != http.StatusOK || token == "" {
			t.Fatalf("pair machine: %d", status)
		}
		m.token = token
		m.connect(d)
		t.Cleanup(func() {
			mirror := filepath.Join(m.botWorkspace("default"), ".dex", "skills")
			_ = filepath.WalkDir(mirror, func(p string, d os.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					return os.Chmod(p, 0o700)
				}
				return err
			})
		})
		chat := d.newSession(t).ID
		d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", chat), map[string]string{"environmentId": m.envID}, nil)
		return m, chat
	}
	first, chat := start("First machine")
	call := func(chat int64, tool string, args map[string]any) string {
		t.Helper()
		return taskTurn(t, d, chat, tool+" "+taskJSON(t, args))
	}
	description := "---\nname: deploy\ndescription: Deploy from a runner\n---\nDeploy.\n"
	writeAndRun := fmt.Sprintf(`
const doc = await tools.skill_write({path:"agent/deploy/SKILL.md", content:%s});
const script = await tools.skill_write({path:"agent/deploy/run.sh", content:%s, mode:"0755"});
const result = await tools.bash({cwd:script.path.slice(0, script.path.lastIndexOf("/")), command:"./run.sh"});
return {doc:doc.path, result};`, taskJSON(t, description), taskJSON(t, "#!/bin/sh\nset -eu\nprintf cached > \"$DEX_SKILL_CACHE/value\"\nif (printf unwanted > ./unwanted) 2>/dev/null; then exit 7; fi\nprintf script-ran\n"))
	// When one script writes files and runs the executable without another prompt render.
	answer := call(chat, "codemode", map[string]any{"code": writeAndRun})
	mirror := filepath.Join(first.botWorkspace("default"), ".dex", "skills", "agent", "deploy")
	if !strings.Contains(answer, "script-ran") || !strings.Contains(answer, filepath.Join(mirror, "SKILL.md")) {
		t.Fatalf("write and execute in one code mode script from %s: %s", mirror, answer)
	}
	store := filepath.Join(dir, "agents", "default", ".agents", "skills")
	if data, err := os.ReadFile(filepath.Join(store, "deploy", "SKILL.md")); err != nil || string(data) != description {
		t.Fatalf("control host SKILL.md = %q, %v", data, err)
	}
	eventually(t, "one skill commit for the chat", func() (bool, any) {
		out, err := exec.CommandContext(t.Context(), "git", "-C", store, "log", "--format=%s").CombinedOutput()
		return strings.TrimSpace(string(out)) == fmt.Sprintf("Update skills from dex:default/chat/%d", chat) && err == nil, string(out)
	})

	// Then the other lease sees the skill on its next request.
	second, other := start("Second machine")
	secondDoc := filepath.Join(second.botWorkspace("default"), ".dex", "skills", "agent", "deploy", "SKILL.md")
	if got := call(other, "read", map[string]any{"path": secondDoc}); !strings.Contains(got, "Deploy from a runner") {
		t.Fatal(got)
	}
	for _, target := range []string{filepath.Join(store, "deploy", "SKILL.md"), filepath.Join(mirror, "SKILL.md")} {
		if got := call(chat, "write", map[string]any{"path": target, "content": "must fail"}); !strings.Contains(got, "skill_*") {
			t.Fatal(got)
		}
		if got := call(chat, "edit", map[string]any{"path": target, "edits": []map[string]string{{"oldText": "Deploy.", "newText": "must fail"}}}); !strings.Contains(got, "skill_*") {
			t.Fatal(got)
		}
	}
	if got := call(chat, "skill_edit", map[string]any{"path": "agent/deploy/SKILL.md", "edits": []map[string]string{{"oldText": "not present", "newText": "must fail"}}}); !strings.HasPrefix(got, "tool error:") {
		t.Fatal(got)
	}

	// The installer writes into a project directory. Import keeps ordinary files,
	// including ignored files, and reports an external link without copying it.
	source := filepath.Join(first.botWorkspace("default"), "installer", "example")
	for rel, body := range map[string]string{
		"SKILL.md": description, "node_modules/pkg/index.js": "module", ".env": "SKILL_FILE_ONLY=value", ".gitignore": "node_modules\n", ".git/config": "do not import",
	} {
		p := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../", filepath.Join(source, "external")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("SKILL.md", filepath.Join(source, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if got := call(chat, "skill_import", map[string]any{"path": source, "level": "dex"}); !strings.HasSuffix(got, "/dex/example") {
		t.Fatal(got)
	}
	if got := call(chat, "skill_list", map[string]any{}); !strings.Contains(got, "example/external") || !strings.Contains(got, `"overrides":true`) {
		t.Fatal(got)
	}
	secondModule := filepath.Join(second.botWorkspace("default"), ".dex", "skills", "dex", "example", "node_modules", "pkg", "index.js")
	if got := call(other, "read", map[string]any{"path": secondModule}); got != "tool: module" {
		t.Fatalf("the open lease did not pick up a host change on its next request: %s", got)
	}
	for _, root := range []string{filepath.Join(dir, ".agents", "skills", "example"), filepath.Join(first.botWorkspace("default"), ".dex", "skills", "dex", "example")} {
		for _, rel := range []string{"node_modules/pkg/index.js", ".env", ".gitignore", "alias.md"} {
			if info, err := os.Lstat(filepath.Join(root, rel)); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("%s/%s = %v, %v", root, rel, info, err)
			}
		}
		for _, rel := range []string{".git", "external"} {
			if _, err := os.Lstat(filepath.Join(root, rel)); !os.IsNotExist(err) {
				t.Fatalf("unexpected %s/%s: %v", root, rel, err)
			}
		}
	}
	if got := call(chat, "bash", map[string]any{"command": `test -z "$SKILL_FILE_ONLY" && cat .dex/cache/skills/deploy/value`}); got != "tool: cached" {
		t.Fatal(got)
	}
	if got := call(chat, "skill_delete", map[string]any{"path": "agent/deploy"}); !strings.HasSuffix(got, "/agent/deploy") {
		t.Fatal(got)
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Fatalf("deleted skill still mirrored: %v", err)
	}
}
