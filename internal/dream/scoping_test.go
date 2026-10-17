package dream_test

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/worker"
)

func TestUserIdleAgentDreamsWhileAnotherAgentIsBusy(t *testing.T) {
	// Given two agents with old, settled chats and one busy agent.
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	// Admission and settlement use the real chat service. Delivery is not started.
	chats := chat.New(conn, nil, clock, nil, nil)
	ids := map[string]int64{}
	for _, id := range []string{"busy", "idle"} {
		if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		if err := q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: id, Name: id, ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
			t.Fatal(err)
		}
		chatID, err := chats.Post(t.Context(), id, "chat:first", "", id+"-first", id+" evidence")
		if err != nil {
			t.Fatal(err)
		}
		ids[id] = chatID
		chats.HandleSettled(t.Context(), worker.Settled{RequestID: id + "-first", Status: "done", Answer: id + " answer"})
	}
	now = now.Add(25 * time.Hour)
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
		t.Fatalf("idle dream: %+v", idle)
	}
	if _, err := q.LatestDream(t.Context(), "busy"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("busy dream: %v", err)
	}
	input, err := q.GetSubmission(t.Context(), idle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.Content, fmt.Sprintf(`"chat":"dex:idle/chat/%d"`, ids["idle"])) || strings.Contains(input.Content, "busy evidence") {
		t.Fatalf("dream input crosses agents or has an old source: %s", input.Content)
	}

	// Then an unfinished dream blocks every other agent, including after a restart.
	chats.HandleSettled(t.Context(), worker.Settled{RequestID: "busy-pending", Status: "done", Answer: "finished"})
	dream.New(q, chats, 24*time.Hour, clock).Check(t.Context())
	if _, err := q.LatestDream(t.Context(), "busy"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("parallel dream admitted: %v", err)
	}
	pending, err := q.ListPendingSubmissions(t.Context())
	if err != nil || len(pending) != 1 || pending[0].ID != idle.ID {
		t.Fatalf("pending dreams: %+v, %v", pending, err)
	}

	// When the first dream settles, the other agent can dream on the next check.
	chats.HandleSettled(t.Context(), worker.Settled{RequestID: idle.ID, Status: "done", Answer: "consolidated"})
	d.Check(t.Context())
	busy, err := q.LatestDream(t.Context(), "busy")
	if err != nil || busy.Status.String != "pending" {
		t.Fatalf("next agent dream: %+v, %v", busy, err)
	}
	// The done idle dream keeps its own boundary and is not repeated.
	latest, err := q.LatestDream(t.Context(), "idle")
	if err != nil || latest.ID != idle.ID {
		t.Fatalf("idle dream repeated: %+v, %v", latest, err)
	}
}
