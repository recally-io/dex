package app_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type deliveredArtifact struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func artifactPath(agentID, name string) string {
	if name == "" {
		return "/api/agents/" + agentID + "/artifacts"
	}
	return "/api/agents/" + agentID + "/artifacts/" + url.PathEscape(name)
}

func listArtifacts(t *testing.T, d *dex, agentID string) []deliveredArtifact {
	t.Helper()
	var out struct {
		Artifacts []deliveredArtifact `json:"artifacts"`
	}
	d.must(t, http.MethodGet, artifactPath(agentID, ""), nil, &out)
	return out.Artifacts
}

func readArtifact(t *testing.T, d *dex, agentID, name string) (http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+artifactPath(agentID, name), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("file: status=%d body=%s err=%v", response.StatusCode, body, err)
	}
	return response.Header, body
}

func deliverArtifact(t *testing.T, d *dex, chatID int64, path string) deliveredArtifact {
	t.Helper()
	answer := taskTool(t, d, chatID, "deliver_file", map[string]string{"path": path})
	var out deliveredArtifact
	if !strings.HasPrefix(answer, "tool: ") {
		t.Fatalf("delivery: %s", answer)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(answer, "tool: ")), &out); err != nil {
		t.Fatalf("delivery: %s: %v", answer, err)
	}
	return out
}

func TestUserManagesBotArtifactDirectory(t *testing.T) {
	t.Parallel()
	// Given a bot with a workspace source and its own artifacts directory.
	data := t.TempDir()
	d := startDex(t, data, "")
	chat := d.newSession(t).ID
	source := filepath.Join(data, "agents", "default", "workspace", "report.md")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("# Original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	item := deliverArtifact(t, d, chat, "report.md")
	file := filepath.Join(data, "agents", "default", "artifacts", "report.md")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("delivery must create the named bot file: %v", err)
	}
	if item.URL != "/api/agents/default/artifacts/report.md" {
		t.Fatalf("file URL=%q", item.URL)
	}
	if err := os.WriteFile(source, []byte("source changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if headers, body := readArtifact(t, d, "default", "report.md"); string(body) != "# Original\n" || headers.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(headers.Get("Content-Disposition"), "report.md") {
		t.Fatalf("copied bytes=%q", body)
	}
	// When the bot repeats a delivery, it gets an explicit conflict instead of replacing a file.
	_ = taskTool(t, d, chat, "deliver_file", map[string]string{"path": "report.md"})
	if _, body := readArtifact(t, d, "default", "report.md"); string(body) != "# Original\n" {
		t.Fatalf("replaced bytes=%q", body)
	}
	d.must(t, http.MethodPatch, artifactPath("default", "report.md"), map[string]string{"name": "renamed.md"}, nil)
	renamed := filepath.Join(filepath.Dir(file), "renamed.md")
	if _, err := os.Stat(renamed); err != nil {
		t.Fatal(err)
	}
	// External directory entries, names, and sizes are the complete list, without registration.
	external := filepath.Join(filepath.Dir(file), "external.md")
	if err := os.Rename(renamed, external); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("disk bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(filepath.Dir(file), "extra.md")
	if err := os.WriteFile(extra, []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	rows := listArtifacts(t, d, "default")
	if len(rows) != 2 || rows[0].Name != "external.md" || rows[0].Size != 10 {
		t.Fatalf("directory=%+v", rows)
	}
	d.must(t, http.MethodPatch, artifactPath("default", "external.md"), map[string]string{"name": "extra.md"}, nil, http.StatusConflict)
	if body, err := os.ReadFile(extra); err != nil || string(body) != "extra" {
		t.Fatalf("rename replaced target=%q err=%v", body, err)
	}
	// Then delete removes just that physical file and keeps the workspace source.
	d.must(t, http.MethodDelete, artifactPath("default", "external.md"), nil, nil)
	if _, err := os.Stat(external); !os.IsNotExist(err) {
		t.Fatalf("file remains: %v", err)
	}
	if body, err := os.ReadFile(source); err != nil || string(body) != "source changed" {
		t.Fatalf("source=%q err=%v", body, err)
	}
	for _, name := range []string{"", "..", "../escape", `a\b`, "bad\nname", strings.Repeat("a", 256)} {
		d.must(t, http.MethodPatch, artifactPath("default", "extra.md"), map[string]string{"name": name}, nil, http.StatusBadRequest)
	}
	d.must(t, http.MethodGet, artifactPath("unknown", "extra.md"), nil, nil, http.StatusNotFound)
}
