package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserForkToolCannotCreateAfterStop(t *testing.T) {
	r := startNative(t, "bash")
	started := filepath.Join(r.dir, "fork-tool-started")
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
	input, err := r.agents.LookupSubmission(t.Context(), engine.Ref{AgentID: "bot", ID: c.ExecutionID}, "request")
	if err != nil || input.Entry == nil {
		t.Fatalf("source entry: %+v %v", input, err)
	}
	entry := input.Entry.ID
	child, err := r.service.ToolFork(t.Context(), source, entry)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); err != nil {
		t.Fatal(err)
	}
	replay, err := r.service.ToolFork(t.Context(), source, entry)
	if err != nil || replay != child {
		t.Fatalf("saved fork replay=%d want=%d err=%v", replay, child, err)
	}
	source.Key = "bot/after-stop"
	if _, err = r.service.ToolFork(t.Context(), source, entry); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("stopped source created fork: %v", err)
	}
	if _, err = r.q.GetForkOperation(t.Context(), source.Key); err == nil {
		t.Fatal("rejected fork saved receipt")
	}
	t.Logf("saved fork %d replays; stopped source cannot create a new fork", child)
}
