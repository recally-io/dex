package app_test

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type directoryListing struct {
	Path    string `json:"path"`
	Parent  string `json:"parent"`
	Home    string `json:"home"`
	Entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Git  bool   `json:"git"`
	} `json:"entries"`
	Truncated bool `json:"truncated"`
}

func TestUserBrowsesHostDirectories(t *testing.T) {
	// Given a host tree with visible, hidden, git, file and symlink entries.
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"Zulu", "alpha/.git", "Beta", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(home, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(home, "Beta", ".git"), "gitdir: elsewhere")
	writeFile(t, filepath.Join(home, "file"), "not a directory")
	if err := os.Symlink(filepath.Join(home, "Zulu"), filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	d := startDex(t, t.TempDir(), "")
	// When the user lists home through each supported path form.
	for _, path := range []string{"", "?path=~", "?path=" + url.QueryEscape("~/./"), "?path=" + url.QueryEscape(home)} {
		var got directoryListing
		d.must(t, http.MethodGet, "/api/directories"+path, nil, &got)
		names := []string{}
		for _, entry := range got.Entries {
			names = append(names, entry.Name)
			if entry.Path != filepath.Join(home, entry.Name) || entry.Git != (entry.Name == "alpha" || entry.Name == "Beta") {
				t.Fatalf("entry: %+v", entry)
			}
		}
		// Then only visible directories appear, in case-insensitive order.
		if !slices.Equal(names, []string{"alpha", "Beta", "linked", "Zulu"}) || got.Path != home || got.Home != home || got.Parent != filepath.Dir(home) || got.Truncated {
			t.Fatalf("listing: %+v", got)
		}
		t.Logf("artifact: listing %s = %+v", path, got)
	}
	var root directoryListing
	d.must(t, http.MethodGet, "/api/directories?path=/", nil, &root)
	if root.Parent != "" {
		t.Fatalf("root parent=%q", root.Parent)
	}
	locked := filepath.Join(home, "locked")
	if err := os.Mkdir(locked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0700) })
	status, body := d.call(t, http.MethodGet, "/api/directories?path="+url.QueryEscape(locked), nil)
	if status != 400 || !strings.Contains(body, "directory cannot be read") || strings.Contains(body, home) {
		t.Fatalf("unreadable: %d %s", status, body)
	}
	// Then invalid paths return short reasons without OS path details.
	for _, path := range []string{"relative", filepath.Join(home, "missing"), filepath.Join(home, "file"), ""} {
		status, body := d.call(t, http.MethodGet, "/api/directories?path="+url.QueryEscape(path), nil)
		if status != 400 || strings.Contains(body, home) {
			t.Fatalf("invalid path %q: %d %s", path, status, body)
		}
	}
}

func TestUserDirectoryListingIsCapped(t *testing.T) {
	// Given more than 500 visible host directories.
	tree := t.TempDir()
	for i := range 501 {
		if err := os.Mkdir(filepath.Join(tree, fmt.Sprintf("dir-%03d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	d := startDex(t, t.TempDir(), "")
	// When the user lists the tree, only the first 500 sorted directories are returned.
	var got directoryListing
	d.must(t, http.MethodGet, "/api/directories?path="+url.QueryEscape(tree), nil, &got)
	if len(got.Entries) != 500 || !got.Truncated || got.Entries[499].Name != "dir-499" {
		t.Fatalf("listing count=%d truncated=%v", len(got.Entries), got.Truncated)
	}
	t.Logf("artifact: count=%d truncated=%v last=%s", len(got.Entries), got.Truncated, got.Entries[499].Name)
}

func TestUserListsRecentAgentProjects(t *testing.T) {
	// Given distinct projects, duplicates, a deleted project and another agent.
	d := startDex(t, t.TempDir(), "")
	conn := sessionDB(t, d)
	var secondID int64
	first, second, deleted, other := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for i, path := range []string{first, second, first, deleted} {
		id := sessionFixture(t, d, fmt.Sprintf("chat:recent-%d", i), int64(i+1))
		d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": path}, nil)
		if path == second {
			secondID = id
		}
	}
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &b, http.StatusCreated)
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO conversations (agent_id,channel_key,created_at,cwd) VALUES (?, 'chat:other', 999, ?)", b.ID, other); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	// When the user lists projects, the newest activity controls order.
	var got struct {
		Projects []string `json:"projects"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/projects", nil, &got)
	if !slices.Equal(got.Projects, []string{first, second}) {
		t.Fatalf("projects=%v", got.Projects)
	}
	// Then later activity moves the older project to the front.
	d.setModel(t, "echo")
	if answer := taskTurn(t, d, secondID, "recent project activity"); answer != "echo: recent project activity" {
		t.Fatal(answer)
	}
	d.must(t, http.MethodGet, "/api/agents/default/projects", nil, &got)
	if !slices.Equal(got.Projects, []string{second, first}) {
		t.Fatalf("projects after activity=%v", got.Projects)
	}
	t.Logf("artifact: recent projects=%v", got.Projects)
	// Then the list remains capped at eight distinct existing paths.
	expected := []string{second}
	for i := range 9 {
		path := t.TempDir()
		id := sessionFixture(t, d, fmt.Sprintf("chat:limit-%d", i), int64(10+i))
		d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": path}, nil)
		expected = slices.Insert(expected, 1, path)
	}
	d.must(t, http.MethodGet, "/api/agents/default/projects", nil, &got)
	if !slices.Equal(got.Projects, expected[:8]) {
		t.Fatalf("capped projects=%v want=%v", got.Projects, expected[:8])
	}
	t.Logf("artifact: capped projects=%v", got.Projects)
}
