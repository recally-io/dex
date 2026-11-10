package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserCompactionStatusDoesNotOpenColdAgent(t *testing.T) {
	r := startNative(t, "echo")
	main, err := r.service.Post(t.Context(), "bot", MainKey, "", "first", "short")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "first")
	if _, err = r.service.Compact(t.Context(), "bot", main, "compact"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		s, err := r.service.CompactionStatus(t.Context(), "bot", main, "compact")
		return err == nil && s.Status == "no_op"
	})
	if err = r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: "bash", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	other, err := r.service.Create(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	starts := filepath.Join(r.dir, "starts")
	marker := filepath.Join(r.dir, "started")
	_, err = r.service.Post(t.Context(), "bot", other.ChannelKey, "", "running", "echo start >> "+starts+"; touch "+marker+"; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	other, err = r.service.Get(t.Context(), "bot", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.service.cancel()
	r.service.wg.Wait()
	if err = r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	service := New(r.conn, next, time.Now, nil, nil)
	defer func() { service.cancel(); service.wg.Wait() }()
	got, err := service.CompactionStatus(t.Context(), "bot", main, "compact")
	if err != nil || got.Status != "no_op" {
		t.Fatalf("status=%+v err=%v", got, err)
	}
	outcome, err := next.RecoveryOutcome(t.Context(), engine.Ref{AgentID: "bot", ID: other.ExecutionID}, "running")
	if err != nil || outcome != nil {
		t.Fatalf("status opened Harness or changed unfinished input: %+v %v", outcome, err)
	}
	data, err := os.ReadFile(starts)
	if err != nil || string(data) != "start\n" {
		t.Fatalf("status resumed work: %q %v", data, err)
	}
	t.Logf("status left the cold agent closed and unfinished work intact; artifact=%s", starts)
}
