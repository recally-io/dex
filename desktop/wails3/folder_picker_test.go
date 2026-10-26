package main

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestFolderRequestsComeOnlyFromTheBackendMainFrame(t *testing.T) {
	backend := "http://127.0.0.1:52100"
	main := &application.OriginInfo{Origin: "http://127.0.0.1:52100/a/default", IsMainFrame: true}
	valid := `dex:pick-folder {"id":"r1","start":"/tmp"}`
	if got, ok := parseFolderRequest(valid, main, backend); !ok || got.ID != "r1" || got.Start != "/tmp" {
		t.Fatalf("valid request = %+v %v", got, ok)
	}
	for name, c := range map[string]struct {
		message string
		origin  *application.OriginInfo
	}{
		"child frame":  {valid, &application.OriginInfo{Origin: main.Origin}},
		"other port":   {valid, &application.OriginInfo{Origin: "http://127.0.0.1:9/", IsMainFrame: true}},
		"other host":   {valid, &application.OriginInfo{Origin: "https://example.com/", IsMainFrame: true}},
		"no origin":    {valid, nil},
		"other prefix": {`dex:settings {"id":"r1"}`, main},
		"no id":        {`dex:pick-folder {"start":"/tmp"}`, main},
		"not json":     {"dex:pick-folder r1", main},
	} {
		if got, ok := parseFolderRequest(c.message, c.origin, backend); ok {
			t.Errorf("%s accepted: %+v", name, got)
		}
	}
}
