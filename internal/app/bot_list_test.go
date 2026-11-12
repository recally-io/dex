package app_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBotListCountsAMainThatIsRunning(t *testing.T) {
	t.Parallel()
	// Given a bot whose Main conversation runs a shell command that waits at a gate.
	d := startDex(t, t.TempDir(), "")
	bot := d.createCoder(t)
	d.setBotModel(t, bot.ID, "bash")
	var main sessionInfo
	d.must(t, http.MethodGet, "/api/agents/"+bot.ID+"/chats/main", nil, &main)
	gate := t.TempDir()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", bot.ID, main.ID),
		map[string]string{"id": uuid.NewString(), "text": taskGate(t, gate, "main")}, nil, http.StatusAccepted)
	waitTaskFile(t, filepath.Join(gate, "main.output"))

	// When the default bot lists the bots.
	got := taskTool(t, d, d.newSession(t).ID, "bot_list", map[string]string{})

	// Then the running bot counts one session, the same session that stops bot_delete.
	var result struct {
		Bots []struct {
			ID      string `json:"id"`
			Running *int64 `json:"runningSessions"`
		} `json:"bots"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "tool: ")), &result); err != nil {
		t.Fatalf("parse: %v %s", err, got)
	}
	counts := map[string]int64{}
	for _, b := range result.Bots {
		if b.Running == nil {
			t.Fatalf("bot %s has no runningSessions: %s", b.ID, got)
		}
		counts[b.ID] = *b.Running
	}
	if counts[bot.ID] != 1 {
		t.Fatalf("bot_list reports %d running sessions for a bot whose Main runs bash: %s", counts[bot.ID], got)
	}
	if err := os.WriteFile(filepath.Join(gate, "main.gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
