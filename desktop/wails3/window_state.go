package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	defaultWindowWidth  = 1280
	defaultWindowHeight = 860
)

type windowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

func windowStatePath(configDir string) string {
	return filepath.Join(configDir, "window.json")
}

func loadWindowState(path string, screens []screenRect) (windowState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return windowState{}, false
	}
	var state windowState
	if json.Unmarshal(data, &state) != nil || state.Width <= 0 || state.Height <= 0 || !windowOnScreen(state, screens) {
		return windowState{}, false
	}
	return state, true
}

func saveWindowState(path string, state windowState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type screenRect struct{ X, Y, Width, Height int }

func windowOnScreen(state windowState, screens []screenRect) bool {
	for _, screen := range screens {
		left := max(state.X, screen.X)
		top := max(state.Y, screen.Y)
		right := min(state.X+state.Width, screen.X+screen.Width)
		bottom := min(state.Y+state.Height, screen.Y+screen.Height)
		if right > left && bottom > top {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
