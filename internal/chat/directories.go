package chat

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type DirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
}

type DirectoryListing struct {
	Path      string           `json:"path"`
	Parent    string           `json:"parent"`
	Home      string           `json:"home"`
	Entries   []DirectoryEntry `json:"entries"`
	Truncated bool             `json:"truncated"`
}

// ListDirectories exposes names and paths, with the same validation as project changes.
func ListDirectories(path *string) (DirectoryListing, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return DirectoryListing{}, fmt.Errorf("%w: home directory is unavailable", ErrProjectInvalid)
	}
	selected := home
	if path != nil {
		selected = *path
		if strings.TrimSpace(selected) == "" {
			return DirectoryListing{}, fmt.Errorf("%w: path must be absolute", ErrProjectInvalid)
		}
	}
	selected, err = ProjectPath(selected)
	if err != nil {
		return DirectoryListing{}, err
	}
	entries, err := os.ReadDir(selected)
	if err != nil {
		return DirectoryListing{}, fmt.Errorf("%w: directory cannot be read", ErrProjectInvalid)
	}
	listing := DirectoryListing{Path: selected, Home: filepath.Clean(home), Parent: filepath.Dir(selected), Entries: []DirectoryEntry{}}
	if listing.Parent == selected {
		listing.Parent = ""
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(selected, entry.Name())
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		git, err := os.Stat(filepath.Join(path, ".git"))
		listing.Entries = append(listing.Entries, DirectoryEntry{Name: entry.Name(), Path: path, Git: err == nil && (git.IsDir() || git.Mode().IsRegular())})
	}
	slices.SortFunc(listing.Entries, func(a, b DirectoryEntry) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.Name, b.Name))
	})
	if len(listing.Entries) > 500 {
		listing.Truncated = true
		listing.Entries = listing.Entries[:500]
	}
	return listing, nil
}
