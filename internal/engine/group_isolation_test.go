package engine

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/mcptest"
)

func TestUserGroupRuntimeNeverLoadsPrivateProfile(t *testing.T) {
	t.Parallel()
	// Given private instructions, AGENTS, skills, memory and environment in a real isolated home.
	data, home, project := t.TempDir(), t.TempDir(), t.TempDir()
	extensionFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "PRIVATE_HOME_RULES")
	extensionFile(t, filepath.Join(data, ".agents", "skills", "private", "SKILL.md"), "---\nname: private\ndescription: PRIVATE_SKILL\n---\nPRIVATE_SKILL")
	extensionFile(t, filepath.Join(data, ".agents", ".env"), "PRIVATE_ENV=PRIVATE_ENV_VALUE\n")
	extensionFile(t, filepath.Join(data, "memory", "MEMORY.md"), "PRIVATE_MEMORY")
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "PRIVATE_PROJECT_RULES")
	a := coreEngine(t, Config{DataDir: data, Home: home})
	options := coreOptions("prompt")
	options.Scope = "tg_123_-100_0"
	options.Cwd = project
	options.Instructions = "PRIVATE_OWNER_INSTRUCTIONS"
	ref := coreCreate(t, a, "alpha", options)
	// When a group input enters native execution, owner options cannot inject resources.
	s, err := a.Submit(t.Context(), ref, Input{RequestID: "group-prompt", Content: "PRIVATE_[A-Z_]+", Origin: "telegram:-100", Source: "group", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	got := coreWait(t, s)
	for _, private := range []string{"PRIVATE_HOME_RULES", "PRIVATE_SKILL", "PRIVATE_ENV_VALUE", "PRIVATE_MEMORY", "PRIVATE_PROJECT_RULES", "PRIVATE_OWNER_INSTRUCTIONS"} {
		if strings.Contains(got.Answer, private) {
			t.Fatalf("group context leaked %s: %s", private, got.Answer)
		}
	}
	if ref.Scope != options.Scope {
		t.Fatalf("group resource identity lost: %+v", ref)
	}
	if _, err := os.Stat(filepath.Join(data, "memory", ".git")); !os.IsNotExist(err) {
		t.Fatalf("group opened the owner's memory repository: %v", err)
	}
}

func TestUserGroupCannotExecutePrivateToolsDirectly(t *testing.T) {
	t.Parallel()
	// Given a group runtime and a provider that sends a direct call despite the offered schema.
	a := coreEngine(t, Config{})
	options := coreOptions("direct")
	options.Scope = "tg_123_-100_0"
	ref := coreCreate(t, a, "alpha", options)
	marker := filepath.Join(t.TempDir(), "escaped")
	input := Input{RequestID: "forbidden", Content: fmt.Sprintf(`bash {"command":%q}`, "printf breached > "+marker), Origin: "telegram:-100", Source: "group", Options: options}
	s, err := a.Submit(t.Context(), ref, input)
	if err != nil {
		t.Fatal(err)
	}
	got := coreWait(t, s)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("group executed host bash: answer=%q err=%v", got.Answer, err)
	}
	if !strings.Contains(got.Answer, "not available") && !strings.Contains(got.Answer, "not allowed") {
		t.Fatalf("forbidden call lacks explicit refusal: %+v", got)
	}
}

func TestUserGroupReadsOnlyExplicitCurrentAudienceResources(t *testing.T) {
	t.Parallel()
	// Given different bot/chat/topic resources and a private file with the same owner.
	data := t.TempDir()
	a := coreEngine(t, Config{DataDir: data})
	private := filepath.Join(data, "agents", "alpha", "workspace", "private.txt")
	extensionFile(t, private, "PRIVATE_FILE")
	scopes := []string{"tg_123_-100_0", "tg_123_-200_0", "tg_123_-100_7", "tg_456_-100_0"}
	refs := make([]Ref, 0, len(scopes))
	for i, scope := range scopes {
		options := coreOptions("direct")
		options.Scope = scope
		ref := coreCreate(t, a, "alpha", options)
		refs = append(refs, ref)
		extensionFile(t, filepath.Join(a.inputDirectory(ref), "public.txt"), fmt.Sprintf("published-%d", i))
	}
	extensionFile(t, filepath.Join(a.inputDirectory(refs[1]), "other.txt"), "OTHER_GROUP_ONLY")
	if err := os.Symlink(private, filepath.Join(a.inputDirectory(refs[0]), "linked.txt")); err != nil {
		t.Fatal(err)
	}
	// When each audience reads the same relative name, native IDs cannot select another scope.
	for i, ref := range refs {
		options := coreOptions("direct")
		options.Scope = ref.Scope
		s, err := a.Submit(t.Context(), ref, Input{RequestID: "published", Content: `group_resource {"path":"public.txt"}`, Options: options})
		if err != nil {
			t.Fatal(err)
		}
		if got := coreWait(t, s).Answer; got != fmt.Sprintf("tool: published-%d", i) {
			t.Fatalf("audience %s read %q", ref.Scope, got)
		}
	}
	for i, path := range []string{"../../workspace/private.txt", "linked.txt", "other.txt", private} {
		options := coreOptions("direct")
		s, err := a.Submit(t.Context(), refs[0], Input{RequestID: fmt.Sprintf("denied-%d", i), Content: fmt.Sprintf(`group_resource {"path":%q}`, path), Options: options})
		if err != nil {
			t.Fatal(err)
		}
		got := coreWait(t, s).Answer
		if !strings.HasPrefix(got, "tool error:") || strings.Contains(got, "PRIVATE_FILE") || strings.Contains(got, "OTHER_GROUP_ONLY") {
			t.Fatalf("resource escape %q: %q", path, got)
		}
	}
	shared, err := a.Submit(t.Context(), refs[0], Input{RequestID: "shared-attachment", Content: "shared", Options: coreOptions("echo"), Attachments: []Attachment{{Path: "public.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, shared).Answer; strings.Contains(got, data) || !strings.Contains(got, "public.txt") {
		t.Fatalf("group attachment leaked the host root: %q", got)
	}
	// Private attachment metadata is rejected before a model request is admitted.
	_, err = a.Submit(t.Context(), refs[0], Input{RequestID: "bad-attachment", Content: "attachment", Options: coreOptions("echo"), Attachments: []Attachment{{Path: "../../workspace/private.txt"}}})
	if err == nil {
		t.Fatal("group admitted a private attachment path")
	}
}

func TestUserGroupChildForkAndRestoreCannotUpgradeResources(t *testing.T) {
	t.Parallel()
	// Given a native group conversation and its fork, without a private bot resource open.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	options := coreOptions("echo")
	options.Scope = "tg_123_-100_4"
	ref := coreCreate(t, a, "alpha", options)
	first, err := a.Submit(t.Context(), ref, Input{RequestID: "first", Content: "group history", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	_ = coreWait(t, first)
	options.Model = ModelRef{Provider: "faux", ModelID: "direct"}
	noteInput := Input{RequestID: "group-note", Content: `group_note {"text":"GROUP_NOTE_PUBLIC"}`, Options: options}
	note, err := a.Submit(t.Context(), ref, noteInput)
	if err != nil {
		t.Fatal(err)
	}
	notePath := strings.TrimPrefix(coreWait(t, note).Answer, "tool: ")
	child, ok, err := a.Fork(t.Context(), ref, 0, false, "")
	if err != nil || !ok {
		t.Fatalf("fork: %+v %v %v", child, ok, err)
	}
	if child.Scope != ref.Scope {
		t.Fatalf("child lost audience: %+v", child)
	}
	marker := filepath.Join(t.TempDir(), "escaped")
	options = coreOptions("direct")
	options.Cwd = t.TempDir()
	options.Instructions = "PRIVATE_RESTORE_INSTRUCTIONS"
	attack := Input{RequestID: "child-attack", Content: fmt.Sprintf(`bash {"command":%q}`, "printf escaped > "+marker), Options: options, Child: true, Automation: true, Main: true}
	submitted, err := a.Submit(t.Context(), child, attack)
	if err != nil {
		t.Fatal(err)
	}
	_ = coreWait(t, submitted)
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When the same stored scoped reference is read and reopened, it cannot fall back to the private store.
	reopened := coreEngine(t, config)
	recovered, err := reopened.RecoveryOutcome(t.Context(), child, attack.RequestID)
	if err != nil || recovered.State != RecoverySettled {
		t.Fatalf("scoped recovery: %+v %v", recovered, err)
	}
	options = coreOptions("prompt")
	options.Cwd = t.TempDir()
	options.Instructions = "PRIVATE_RESTORE_INSTRUCTIONS"
	s, err := reopened.Submit(t.Context(), child, Input{RequestID: "restored", Content: "PRIVATE_[A-Z_]+", Options: options, Child: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, s).Answer; strings.Contains(got, "PRIVATE_") {
		t.Fatalf("restored child leaked owner input: %s", got)
	}
	scopedRead, err := reopened.Submit(t.Context(), child, Input{RequestID: "read-note", Content: fmt.Sprintf(`group_resource {"path":%q}`, notePath), Options: coreOptions("direct"), Child: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, scopedRead).Answer; got != "tool: GROUP_NOTE_PUBLIC" {
		t.Fatalf("group note did not restore in its audience: %q", got)
	}
	replay, err := reopened.Submit(t.Context(), ref, noteInput)
	if err != nil {
		t.Fatal(err)
	}
	_ = coreWait(t, replay)
	notes, err := filepath.Glob(filepath.Join(reopened.inputDirectory(ref), "note-*.txt"))
	if err != nil || len(notes) != 1 {
		t.Fatalf("group note replay created duplicates: %v %v", notes, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child executed host tool: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.DataDir, "memory", ".git")); !os.IsNotExist(err) {
		t.Fatalf("child opened private memory: %v", err)
	}
}

func TestUserGroupRejectsSymlinkedResourceAndExecutionRoots(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"scope", "resources", "state.sqlite"} {
		t.Run(target, func(t *testing.T) {
			data := t.TempDir()
			scope := "tg_123_-100_0"
			dir := filepath.Join(data, "agents", "alpha", "telegram-groups", scope)
			outside := t.TempDir()
			if target == "scope" {
				if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				destination := outside
				if target == "state.sqlite" {
					destination = filepath.Join(outside, "private.sqlite")
					extensionFile(t, destination, "PRIVATE_DATABASE")
				}
				if err := os.Symlink(destination, filepath.Join(dir, target)); err != nil {
					t.Fatal(err)
				}
			}
			a := coreEngine(t, Config{DataDir: data})
			options := coreOptions("echo")
			options.Scope = scope
			if _, err := a.Create(t.Context(), "alpha", options); err == nil {
				t.Fatalf("group opened symlinked %s", target)
			}
		})
	}
}

func TestUserGroupDoesNotStartPrivateMCPOrCaptureOwnerEnvironment(t *testing.T) {
	t.Parallel()
	// Given real MCP configuration at each private level and a login environment boundary.
	data, home := t.TempDir(), t.TempDir()
	var contacts, captures atomic.Int64
	server := &mcptest.Server{Dir: t.TempDir(), Prefix: "private"}
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacts.Add(1); server.ServeHTTP(w, r) }))
	t.Cleanup(boundary.Close)
	settings, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"private": map[string]any{"type": "http", "url": boundary.URL}}})
	for _, level := range []string{filepath.Join(home, ".agents"), filepath.Join(data, ".agents"), filepath.Join(data, "agents", "alpha", ".agents")} {
		extensionFile(t, filepath.Join(level, "mcp.json"), string(settings))
	}
	a := coreEngine(t, Config{DataDir: data, Home: home, LoginEnv: func(_ context.Context, base map[string]string) (map[string]string, error) {
		captures.Add(1)
		return base, nil
	}})
	options := coreOptions("echo")
	options.Scope = "tg_123_-100_0"
	ref := coreCreate(t, a, "alpha", options)
	s, err := a.Submit(t.Context(), ref, Input{RequestID: "group", Content: "hello group", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	_ = coreWait(t, s)
	// Then initialization and status checks neither connect to private tools nor load their environment.
	if stale := a.StaleExtensions(t.Context()); len(stale) != 0 {
		t.Fatalf("group is tracked as private extension resources: %v", stale)
	}
	if contacts.Load() != 0 || captures.Load() != 0 {
		t.Fatalf("private resources touched: mcp=%d environment=%d", contacts.Load(), captures.Load())
	}
}
