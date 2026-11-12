package app_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"uuid"
)

func TestUserSteerKeepsChannelRunRecipient(t *testing.T) {
	t.Parallel()
	e := newMainEntry(t, "telegram")
	d := e.d
	d.setModel(t, "bash")
	main := d.mainChat(t)
	conn := sessionDB(t, d)
	run := newGatedRun(t, "channel")
	e.send(run.command)
	run.waitStarted(t)
	var first string
	if err := conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE origin=? AND kind='message'", e.peer).Scan(&first); err != nil {
		t.Fatal(err)
	}
	steer := uuid.NewV7().String()
	sendMode(t, d, main, steer, "echo steered", "steer", http.StatusAccepted)
	w := d.watch(t, main)
	w.until(t, "steer queued", queuedSteer(steer))
	run.open()
	e.wait(t, "bash: steered")
	for _, id := range []string{first, steer} {
		waitResultSubmission(t, conn, id, "done")
	}
	w.until(t, "steered answer", answered("bash: steered"))
	if got := assistantTexts(&w.tr); !slices.Equal(got, []string{"bash: steered"}) {
		t.Fatalf("run answers = %+v", got)
	}
	e.send("echo delivery-barrier")
	e.wait(t, "bash: delivery-barrier")
	if got := e.sends(); !slices.Equal(got, []channelSend{{Peer: e.peer, Text: "bash: steered"}, {Peer: e.peer, Text: "bash: delivery-barrier"}}) {
		t.Fatalf("channel replies = %+v", got)
	}
}

func TestUserSteerCannotGrantBotAuthorityToChildResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	main := d.mainChat(t)
	conn := sessionDB(t, d)
	child := newGatedRun(t, "child")
	d.send(t, main, childJSON(t, map[string]string{"title": "gated child", "message": child.command, "provider": "faux", "modelId": "bash"}))
	child.waitStarted(t)
	d.waitSettled(t, main)
	run := newGatedRun(t, "result")
	args, err := json.Marshal(map[string]string{"command": run.command})
	if err != nil {
		t.Fatal(err)
	}
	content := "bash " + string(args)
	// The faux model reads the child result as a gated bash command.
	if _, err := conn.ExecContext(t.Context(), `CREATE TRIGGER gate_child_result AFTER INSERT ON submissions WHEN NEW.kind='task_result' BEGIN UPDATE submissions SET content=`+"'"+strings.ReplaceAll(content, "'", "''")+"'"+` WHERE id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	d.setModel(t, "tool")
	child.open()
	run.waitStarted(t)
	var resultID, origin, kind string
	if err := conn.QueryRowContext(t.Context(), "SELECT id, origin, kind FROM submissions WHERE conversation_id=? AND kind='task_result'", main).Scan(&resultID, &origin, &kind); err != nil {
		t.Fatal(err)
	}
	if origin != "web" || kind != "task_result" {
		t.Fatalf("child result = %s %s", origin, kind)
	}
	steer := uuid.NewV7().String()
	sendMode(t, d, main, steer, `bot_create {"name":"Unauthorized","provider":"faux","modelId":"echo"}`, "steer", http.StatusAccepted)
	w := d.watch(t, main)
	w.until(t, "steer queued", queuedSteer(steer))
	run.open()
	w.until(t, "bot tool refused", func(tr *transcript) bool {
		return !tr.running && slices.ContainsFunc(tr.items, func(it item) bool {
			return it.Kind == "tool" && it.Name == "bot_create" && it.Status == "error" && strings.Contains(it.Output, "bot tool not allowed here")
		})
	})
	waitResultSubmission(t, conn, resultID, "done")
	waitResultSubmission(t, conn, steer, "done")
	var agents struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	d.must(t, http.MethodGet, "/api/agents", nil, &agents)
	for _, agent := range agents.Agents {
		if agent.Name == "Unauthorized" {
			t.Fatal("the steer granted bot creation authority")
		}
	}
}
