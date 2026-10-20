package main

import (
	"context"
	"embed"
	"log"
	"time"

	"github.com/egoist/mygo"
)

//go:embed all:dist
var frontend embed.FS

type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// ProgressEvent is the broadcast shape used to compare with Dex live events.
var ProgressEvent = mygo.NewEvent[Progress]("agent:progress")

// Agent is the smallest typed binding surface needed by the spike.
type Agent struct{}

func (Agent) Ping(_ context.Context, text string) (string, error) {
	return "pong: " + text, nil
}

// Stream demonstrates a per-call typed channel for incremental output.
func (Agent) Stream(ctx context.Context, topic string, out *mygo.Channel[Progress]) error {
	for i := 1; i <= 3; i++ {
		p := Progress{Phase: topic, Done: i, Total: 3}
		if err := out.Send(p); err != nil {
			return err
		}
		ProgressEvent.Broadcast(p)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return nil
}

func main() {
	mygo.SetFrontend(frontend)
	mygo.Bind(Agent{})
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	mygo.App.OnSecondInstance(func([]string, string) {})
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{Title: "Dex MyGo Spike", Width: 720, Height: 480, URL: "/"})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
