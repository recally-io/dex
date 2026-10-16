package app_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

func TestUserDreamMemoryExcludesInternalTaskInput(t *testing.T) {
	// Given a completed delegation and ordinary user JSON with the same shape.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Memory boundary", "goal": "saved evidence", "provider": "faux", "modelId": "echo"}))
	task := waitExecution(t, conn, parent, "Memory boundary", "done")
	result := waitResultSubmission(t, conn, "task:"+task.ID+":result", "done")
	setTaskChatModel(t, d, parent, "echo")
	ordinary := `{"task-result":{"report":"ordinary user JSON"}}`
	if got := taskTurn(t, d, parent, ordinary); got != "echo: "+ordinary {
		t.Fatal(got)
	}
	// When a real dream records those settled chats in the memory repository.
	d.setModel(t, "bash")
	d.stop()
	startDexWith(t, app.Config{DataDir: dir, DreamEvery: time.Millisecond})
	var text string
	eventually(t, "dream includes the ordinary user record", func() (bool, any) {
		data, err := os.ReadFile(filepath.Join(dir, "memory", "dreams.md"))
		text = string(data)
		return err == nil && strings.Contains(text, strconv.Quote(ordinary)), text
	})
	// Then internal result input is absent, while the user's JSON stays in its record.
	if strings.Contains(text, strconv.Quote(result.content)) {
		t.Fatalf("dream treated internal result input as user text: %s", text)
	}
	t.Logf("execution=%s ordinary_user_kept=true internal_result_excluded=true", task.ID)
}
