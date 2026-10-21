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

func TestUserMaximisedWindowKeepsNormalFrame(t *testing.T) {
	path := t.TempDir() + "/window.json"
	normal := windowState{X: 100, Y: 80, Width: 1100, Height: 760}
	if err := recordWindowState(path, normal); err != nil {
		t.Fatal(err)
	}
	if err := recordWindowState(path, windowState{X: 0, Y: 25, Width: 1920, Height: 1055, Maximised: true}); err != nil {
		t.Fatal(err)
	}
	got, ok := loadWindowState(path, []screenRect{{X: 0, Y: 0, Width: 1920, Height: 1080}})
	want := normal
	want.Maximised = true
	if !ok || got != want {
		t.Fatalf("got %+v, ok %v; want %+v", got, ok, want)
	}
}

func TestUserClosingWindowKeepsLastFrame(t *testing.T) {
	path := t.TempDir() + "/window.json"
	last := windowState{X: 100, Y: 80, Width: 1100, Height: 760}
	if err := recordWindowState(path, last); err != nil {
		t.Fatal(err)
	}
	if err := recordWindowState(path, windowState{X: 100, Y: 80}); err != nil {
		t.Fatal(err)
	}
	got, ok := loadWindowState(path, []screenRect{{X: 0, Y: 0, Width: 1920, Height: 1080}})
	if !ok || got != last {
		t.Fatalf("got %+v, ok %v; want %+v", got, ok, last)
	}
}
