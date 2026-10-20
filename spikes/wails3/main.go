package main

import (
	"context"
	"embed"
	"log"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed frontend/dist/*
var assets embed.FS

type TranscriptService struct {
	seq atomic.Uint64
}

func (s *TranscriptService) Ping() string { return "pong" }

func (s *TranscriptService) ServiceStartup(context.Context, application.ServiceOptions) error {
	log.Print("service startup")
	return nil
}

func (s *TranscriptService) ServiceShutdown() error {
	log.Print("service shutdown")
	return nil
}

func main() {
	service := &TranscriptService{}
	app := application.New(application.Options{
		Name:     "Dex Wails 3 Spike",
		Services: []application.Service{application.NewService(service)},
		Assets:   application.AssetOptions{Handler: application.BundledAssetFileServer(assets)},
		Mac:      application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})

	app.HandleStream("transcript", func(c *application.StreamConn) {
		defer c.Close()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.SendJSON(map[string]any{"seq": service.seq.Add(1), "text": "spike transcript chunk"}); err != nil {
					return
				}
			case <-c.Context().Done():
				return
			}
		}
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "Dex Wails 3 Spike", Width: 720, Height: 480, URL: "/"})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
