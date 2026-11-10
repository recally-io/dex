package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserCompactToolCannotCreateAfterStop(t *testing.T) {
	r := startNative(t, "bash")
	started := filepath.Join(r.dir, "compact-tool-started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "request", "touch "+started+"; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	source := engine.TaskContext{AgentID: "bot", ConversationID: c.ExecutionID, SourceRequestID: "request", Key: "bot/first"}
	receipt, err := r.service.ToolCompact(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); err != nil {
		t.Fatal(err)
	}
	replay, err := r.service.ToolCompact(t.Context(), source)
	if err != nil || replay.ID != receipt.ID {
		t.Fatalf("replay=%+v want=%+v err=%v", replay, receipt, err)
	}
	source.Key = "bot/after-stop"
	if _, err = r.service.ToolCompact(t.Context(), source); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("stopped source created compact: %v", err)
	}
	if _, err = r.q.GetCompactOperation(t.Context(), "bot:after-stop"); err == nil {
		t.Fatal("rejected compact saved receipt")
	}
	t.Logf("saved operation %s replays; stopped source cannot create a new one", receipt.ID)
}
