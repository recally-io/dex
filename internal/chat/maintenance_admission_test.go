package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

func executionRef(c Chat) engine.Ref { return engine.Ref{AgentID: "bot", ID: c.ExecutionID} }

func TestUserStopWithdrawsQueuedInputAndClearRefusesBusy(t *testing.T) {
	r := startNative(t, "bash")
	marker := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "first", "touch "+marker+"; sleep 60")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	output := filepath.Join(r.dir, "forbidden")
	if _, err = r.service.Post(t.Context(), "bot", MainKey, "", "queued", "touch "+output); err != nil {
		t.Fatal(err)
	}
	if err = r.service.Clear(t.Context(), "bot", id, "busy-reset", "web"); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("clear busy: %v", err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); err != nil {
		t.Fatal(err)
	}
	if sub := receipt(t, r, "queued"); sub.Reason.String != "stopped" {
		t.Fatalf("queued receipt: %+v", sub)
	}
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.agents.CheckIdle(t.Context(), executionRef(c)) == nil })
	if _, err = os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("withdrawn input executed: %v", err)
	}
	if _, err = r.service.Post(t.Context(), "bot", MainKey, "", "next", "printf next"); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "next")
	if _, err = os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("withdrawn input resumed: %v", err)
	}
}
