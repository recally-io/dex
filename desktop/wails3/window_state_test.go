package main

import "testing"

func TestUserWindowStateSaveLoadRoundTrip(t *testing.T) {
	path := t.TempDir() + "/window.json"
	want := windowState{X: 20, Y: 30, Width: 1280, Height: 860, Maximised: true}
	if err := saveWindowState(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadWindowState(path, []screenRect{{X: 0, Y: 0, Width: 1920, Height: 1080}})
	if !ok || got != want {
		t.Fatalf("got %+v, ok %v; want %+v", got, ok, want)
	}
}

func TestUserWindowStateOffScreenFallsBack(t *testing.T) {
	path := t.TempDir() + "/window.json"
	if err := saveWindowState(path, windowState{X: 3000, Y: 2000, Width: 1280, Height: 860}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadWindowState(path, []screenRect{{X: 0, Y: 0, Width: 1920, Height: 1080}}); ok {
		t.Fatal("off-screen window state was accepted")
	}
}
