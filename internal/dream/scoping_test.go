package dream_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserIdleAgentDreamsWhileAnotherAgentIsBusy(t *testing.T) {
	// Given real native execution for two agents, with old settled chats and one active input.
	data := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	var now atomic.Int64
	now.Store(time.Unix(1_800_000_000, 0).UnixMilli())
	clock := func() time.Time { return time.UnixMilli(now.Load()) }
	agents, err := engine.New(engine.Config{DataDir: data, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	busyRelease, dreamRelease := make(chan struct{}), make(chan struct{})
	var dreams atomic.Int64
	faux := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "dream-fixture", Models: []ai.FauxModelDefinition{{ID: "echo"}}})
	step := ai.FauxFactoryStep(func(request ai.TranscriptContext, options ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
		var text string
		for _, message := range request.Messages() {
			if user, ok := message.(ai.UserMessage); ok {
				if content, ok := user.Content.(ai.UserText); ok {
					text = string(content)
				}
			}
		}
		var gate <-chan struct{}
		if text == "working" {
			gate = busyRelease
		}
		if strings.HasPrefix(text, "Chats since the previous dream") && dreams.Add(1) == 1 {
			gate = dreamRelease
		}
		if gate != nil {
			select {
			case <-gate:
			case <-options.Signal.Done():
				return ai.AssistantMessage{}, context.Cause(options.Signal)
			}
		}
		return ai.FauxResponse{Content: []ai.FauxContentBlock{ai.FauxText("consolidated")}}.AssistantMessage(), nil
	})
	responses := make([]ai.FauxResponseStep, 8)
	for i := range responses {
		responses[i] = step
	}
	faux.SetResponses(responses)
	agents.Catalog.Models.SetProvider(faux.Provider())
	chats := chat.New(conn, agents, clock, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); chats.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if err := agents.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	waitDone := func(id string) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			row, err := q.GetSubmission(t.Context(), id)
			if err == nil && row.Status == "done" {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("input %s did not settle: status=%s err=%v", id, row.Status, err)
			case <-tick.C:
			}
		}
	}
	ids := map[string]int64{}
	for _, id := range []string{"busy", "idle"} {
		if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		if err := q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: id, Name: id, ModelProvider: "dream-fixture", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
			t.Fatal(err)
		}
		chatID, err := chats.Post(t.Context(), id, "chat:first", "", id+"-first", id+" evidence")
		if err != nil {
			t.Fatal(err)
		}
		ids[id] = chatID
		waitDone(id + "-first")
	}
	now.Add((25 * time.Hour).Milliseconds())
	if _, err := chats.Post(t.Context(), "busy", "chat:active", "", "busy-pending", "working"); err != nil {
		t.Fatal(err)
	}
	d := dream.New(q, chats, 24*time.Hour, clock)

	// When concurrent checks run, only the idle agent gets one dream.
	var checks sync.WaitGroup
	for range 8 {
		checks.Go(func() { d.Check(t.Context()) })
	}
	checks.Wait()
	idle, err := q.LatestDream(t.Context(), "idle")
	if err != nil {
		t.Fatal(err)
	}
	if idle.Status.String != "pending" {
		t.Fatalf("idle dream status: %s", idle.Status.String)
	}
	if _, err := q.LatestDream(t.Context(), "busy"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("busy dream: %v", err)
	}
	input, err := q.GetSubmission(t.Context(), idle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.Content, fmt.Sprintf(`"chat":"dex:idle/chat/%d"`, ids["idle"])) || strings.Contains(input.Content, "busy evidence") {
		t.Fatal("dream input crosses agents or has an old source")
	}

	// Then the unfinished dream blocks another agent even after its foreground input settles.
	close(busyRelease)
	waitDone("busy-pending")
	dream.New(q, chats, 24*time.Hour, clock).Check(t.Context())
	if _, err := q.LatestDream(t.Context(), "busy"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("parallel dream admitted: %v", err)
	}
	pending, err := q.ListPendingSubmissions(t.Context())
	if err != nil || len(pending) != 1 || pending[0].ID != idle.ID {
		t.Fatalf("pending dream count=%d err=%v", len(pending), err)
	}

	// When the native dream finishes, the next check can admit the other agent.
	close(dreamRelease)
	waitDone(idle.ID)
	d.Check(t.Context())
	busy, err := q.LatestDream(t.Context(), "busy")
	if err != nil {
		t.Fatalf("next agent dream: %v", err)
	}
	waitDone(busy.ID)
	latest, err := q.LatestDream(t.Context(), "idle")
	if err != nil || latest.ID != idle.ID {
		t.Fatalf("idle dream repeated: %v", err)
	}
}
