package app_test

import (
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUserReadsSafeLiveToolsOfChildInAnotherBot(t *testing.T) {
	// Given a child in another bot that prints its private environment and keeps running.
	bots := startBots(t)
	parent := bots.chat("default")
	started := filepath.Join(t.TempDir(), "started")
	child := parent.newChild(map[string]any{
		"bot": bots.b.ID, "title": "wait", "provider": "faux", "modelId": "bash",
		"message": fmt.Sprintf("printf '%%s' \"$CROSS_MARKER\"; echo started > %q; while :; do sleep 1; done", started),
	})
	waitTaskFile(t, started)
	defer parent.operate("session_stop", map[string]any{"id": child.ID})

	// When the parent reads the child while its tool is running.
	deadline := time.Now().Add(10 * time.Second)
	for {
		answer := parent.call("session_read", map[string]any{"id": child.ID})
		var page struct {
			BotID    string `json:"botId"`
			Activity struct {
				Available    bool `json:"available"`
				RunningTools []struct {
					Name            string `json:"name"`
					FirstObservedAt int64  `json:"firstObservedAt"`
				} `json:"runningTools"`
			} `json:"activity"`
		}
		toolJSON(t, answer, &page)
		if len(page.Activity.RunningTools) > 0 {
			// Then the result names the tool and its start, but not its output.
			if page.BotID != bots.b.ID || !page.Activity.Available || page.Activity.RunningTools[0].Name != "bash" || page.Activity.RunningTools[0].FirstObservedAt == 0 {
				t.Fatalf("live tools: %+v", page)
			}
			var raw map[string]any
			toolJSON(t, answer, &raw)
			encoded, err := json.Marshal(raw["activity"])
			if err != nil {
				t.Fatal(err)
			}
			activity := string(encoded)
			fields, ok := raw["activity"].(map[string]any)
			if !ok || len(fields) != 3 || fields["available"] == nil || fields["lastObservedAt"] == nil || fields["runningTools"] == nil {
				t.Fatalf("unexpected activity fields: %s", activity)
			}
			if strings.Contains(activity, "from-b") || strings.Contains(activity, "CROSS_MARKER") || strings.Contains(activity, "command") {
				t.Fatalf("unsafe live tools: %s", activity)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live tool: %s", answer)
		}
	}
}
