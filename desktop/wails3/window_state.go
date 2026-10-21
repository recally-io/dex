package main

import (
	"encoding/json"
	"errors"
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

// recordWindowState saves the current frame. A maximised window keeps the last normal frame, so
// the window returns to that size when the user unmaximises it after a relaunch.
func recordWindowState(path string, current windowState) error {
	// A closing window reports a zero frame. Saving it would replace the last real frame.
	if current.Width <= 0 || current.Height <= 0 {
		return nil
	}
	if current.Maximised {
		var saved windowState
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &saved) == nil && saved.Width > 0 && saved.Height > 0 {
			saved.Maximised = true
			current = saved
		}
	}
	return saveWindowState(path, current)
}

func saveWindowState(path string, state windowState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// Move and resize events save at the same time, so each save writes its own temporary file.
	tmp, err := os.CreateTemp(filepath.Dir(path), "window-*.json")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
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
