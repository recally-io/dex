package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/api"
)

func TestUserInspectsMonitorsAndOutboxViaAPI(t *testing.T) {
	t.Parallel()
	// Given a session where the bot schedules a follow-up monitor via tool call.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	sessionID := d.newSession(t).ID
	d.send(t, sessionID, `monitor_set {"delaySeconds":300,"prompt":"check build status"}`)
	d.waitSettled(t, sessionID)

	// When the user queries monitors and outbox for the session.
	var monitorsResp struct {
		Monitors []struct {
			ID         string `json:"id"`
			Prompt     string `json:"prompt"`
			State      string `json:"state"`
			ChainDepth int    `json:"chainDepth"`
		} `json:"monitors"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d/monitors", sessionID), nil, &monitorsResp)
	if len(monitorsResp.Monitors) != 1 || monitorsResp.Monitors[0].State != "scheduled" || monitorsResp.Monitors[0].Prompt != "check build status" {
		t.Fatalf("unexpected monitors: %+v", monitorsResp.Monitors)
	}

	var outboxResp struct {
		Messages []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"messages"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d/outbox", sessionID), nil, &outboxResp)
	if len(outboxResp.Messages) != 0 {
		t.Fatalf("unexpected outbox: %+v", outboxResp.Messages)
	}

	// When the user cancels the scheduled monitor via DELETE.
	var cancelled struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	d.must(t, http.MethodDelete, fmt.Sprintf("/api/bots/default/sessions/%d/monitors/%s", sessionID, monitorsResp.Monitors[0].ID), nil, &cancelled)
	if cancelled.State != "cancelled" {
		t.Fatalf("expected cancelled monitor, got %+v", cancelled)
	}

	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d/monitors", sessionID), nil, &monitorsResp)
	if len(monitorsResp.Monitors) != 1 || monitorsResp.Monitors[0].State != "cancelled" {
		t.Fatalf("expected cancelled state in list, got %+v", monitorsResp.Monitors)
	}
}

func TestUserKeepsLegacyTimerHistoryAndFiresDueWorkOnceAfterUpgrade(t *testing.T) {
	t.Parallel()
	// Given a native session and control database from before the Monitor migration.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	sessionID := d.newSession(t).ID
	d.send(t, sessionID, "schedule fixture")
	d.waitSettled(t, sessionID)
	d.stop()
	path := filepath.Join(dir, "control.sqlite")
	snapshot := filepath.Join(dir, "control-fixture.sqlite")
	if err := os.Rename(path, snapshot); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, os.DirFS("../db/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	// Build the old schema and link it to the native conversation from the fixture.
	if _, err := provider.UpTo(t.Context(), 20261122110000); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "ATTACH DATABASE ? AS fixture", snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
INSERT INTO agents (id, name, model_provider, model_id, created_at)
SELECT id, name, model_provider, model_id, created_at FROM fixture.bots;
INSERT INTO conversations (id, agent_id, channel_key, execution_id, created_at, title)
SELECT id, bot_id, channel_key, execution_id, created_at, title FROM fixture.sessions;
INSERT INTO submissions (id, conversation_id, content, origin, kind, status, created_at, admitted_at, settled_at, answer)
SELECT id, session_id, content, origin, kind, status, created_at, admitted_at, settled_at, answer FROM fixture.submissions;
DETACH DATABASE fixture;`); err != nil {
		t.Fatal(err)
	}
	var root string
	if err := conn.QueryRowContext(t.Context(), `SELECT id FROM submissions WHERE conversation_id = ? AND kind = 'message'`, sessionID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Second).UnixMilli()
	for _, f := range []struct {
		id, state, prompt string
		fire              sql.NullString
	}{
		{id: "legacy-done", state: "fired", prompt: "old completed prompt", fire: sql.NullString{String: "timer:legacy-done:fire", Valid: true}},
		{id: "legacy-armed", state: "scheduled", prompt: "migrated follow-up"},
		{id: "legacy-cancelled", state: "cancelled", prompt: "old cancelled prompt"},
	} {
		_, err = conn.ExecContext(t.Context(), `INSERT INTO followup_timers (id, operation_key, agent_id, conversation_id, root_request_id, source_submission_id, chain_depth, due_at, time_zone, prompt, state, fire_submission_id, created_at, updated_at) VALUES (?, ?, 'default', ?, ?, ?, 2, ?, 'UTC', ?, ?, ?, ?, ?)`, f.id, "operation:"+f.id, sessionID, root, root, now, f.prompt, f.state, f.fire, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	// When Dex upgrades and the real native admission dispatcher processes the due prompt.
	d = startDex(t, dir, "")
	eventually(t, "migrated follow-up receipt", func() (bool, any) {
		var status string
		err := conn.QueryRowContext(t.Context(), `SELECT status FROM submissions WHERE id = 'monitor:legacy-armed:1'`).Scan(&status)
		return err == nil && status == "done", status
	})
	d.stop()
	// Then a second startup still exposes all three records and does not replay either prompt.
	d = startDex(t, dir, "")
	var list api.MonitorList
	d.must(t, http.MethodGet, sessionPath(sessionID, "monitors"), nil, &list)
	if len(list.Monitors) != 3 {
		t.Fatalf("expected the three legacy records: %+v", list.Monitors)
	}
	for _, got := range list.Monitors {
		wantState, wantRuns := "fired", 1
		if got.Id == "legacy-cancelled" {
			wantState, wantRuns = "cancelled", 0
		}
		if got.Mode != "prompt" || got.MaxRuns != 1 || string(got.State) != wantState || got.Runs != wantRuns || got.DueAt != now || got.ChainDepth != 2 {
			t.Fatalf("legacy record changed: %+v", got)
		}
	}
	var completed, operation, source string
	if err := conn.QueryRowContext(t.Context(), `SELECT fire_submission_id, operation_key, source_submission_id FROM monitors WHERE id = 'legacy-done'`).Scan(&completed, &operation, &source); err != nil || completed != "timer:legacy-done:fire" || operation != "operation:legacy-done" || source != root {
		t.Fatalf("legacy identity was lost: %q %q %q %v", completed, operation, source, err)
	}
	var total int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE session_id = ? AND kind = 'wake'`, sessionID).Scan(&total); err != nil || total != 1 {
		t.Fatalf("expected one follow-up across both boots, got %d (%v)", total, err)
	}
	d.watch(t, sessionID).until(t, "Monitor wake in the transcript after restart", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(mark item) bool {
			return mark.ID == "monitor:legacy-armed:1" && mark.Kind == "wake" && mark.Title == "Monitor" && mark.Status == "due" && mark.Text == ""
		})
	})
	d.must(t, http.MethodGet, sessionPath(sessionID, "timers"), nil, nil, http.StatusNotFound)
}
