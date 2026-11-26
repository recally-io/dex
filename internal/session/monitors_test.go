package session

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserMonitorsBoundsChainDepthAndAuthority(t *testing.T) {
	r := startNative(t, "echo")
	now := r.service.now()
	r.service.now = func() time.Time { return now }
	conv, _ := taskParent(t, r)
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "user-root-1", "check later"); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, "user-root-1")); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "user-root-1")
	cRow, err := r.q.GetSession(t.Context(), conv)
	if err != nil {
		t.Fatal(err)
	}

	call := engine.TaskContext{
		AgentID: "bot", ConversationID: cRow.ExecutionID.Int64,
		SourceRequestID: "user-root-1", Key: "bot/t-invalid-1",
	}
	// Rejects delay < 60s and > 7 days.
	if _, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 30, Prompt: "too soon"}); err == nil {
		t.Fatal("expected error for delay < 60s")
	}
	if _, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 8 * 24 * 3600, Prompt: "too far"}); err == nil {
		t.Fatal("expected error for delay > 7d")
	}

	// Enforces 32 active monitors per conversation and allows cancel + re-add.
	var firstMonitorID string
	for i := range maxActiveMonitors {
		call.Key = fmt.Sprintf("bot/t-cap-%d", i)
		info, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 120, Prompt: fmt.Sprintf("check %d", i)})
		if err != nil {
			t.Fatalf("monitor %d failed: %v", i, err)
		}
		if i == 0 {
			firstMonitorID = info.ID
		}
		if info.ChainDepth != 0 {
			t.Fatalf("expected depth 0 from user turn, got %d", info.ChainDepth)
		}
	}
	call.Key = "bot/t-cap-overflow"
	if _, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 120, Prompt: "overflow"}); err == nil {
		t.Fatal("expected active monitor cap error")
	}
	cancelled, err := r.service.ToolMonitorCancel(t.Context(), call, firstMonitorID)
	if err != nil || cancelled.State != "cancelled" {
		t.Fatalf("cancel monitor: info=%+v err=%v", cancelled, err)
	}

	// Verify chain depth 0 -> 6 succeeds and depth 7 is rejected.
	sourceReq := "user-root-1"
	for depth := range maxMonitorChainDepth + 1 {
		call.Key = fmt.Sprintf("bot/t-chain-%d", depth)
		call.SourceRequestID = sourceReq
		info, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 120, Prompt: fmt.Sprintf("depth %d", depth)})
		if err != nil {
			t.Fatalf("chain depth %d failed: %v", depth, err)
		}
		now = now.Add(121 * time.Second)
		if err := r.service.FireDueMonitors(t.Context()); err != nil {
			t.Fatal(err)
		}
		sourceReq = "monitor:" + strings.TrimPrefix(info.ID, "schedule-") + ":1"
		// Fire submission cannot call change=true bot tools.
		if err := r.service.ToolBotCaller(t.Context(), engine.TaskContext{
			AgentID: "bot", ConversationID: cRow.ExecutionID.Int64,
			SourceRequestID: sourceReq, Key: fmt.Sprintf("bot/botcheck-%d", depth),
		}, true); err == nil {
			t.Fatal("expected ToolBotCaller to reject monitor submission")
		}
	}
	call.Key = "bot/t-chain-exceeded"
	call.SourceRequestID = sourceReq
	if _, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 120, Prompt: "depth 7"}); err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("expected chain depth limit error, got %v", err)
	}
}

func TestUserMonitorsExpiryAndCancelHooks(t *testing.T) {
	r := startNative(t, "echo")
	now := r.service.now()
	r.service.now = func() time.Time { return now }
	conv, _ := taskParent(t, r)
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "user-root-2", "watch build"); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, "user-root-2")); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "user-root-2")
	cRow, err := r.q.GetSession(t.Context(), conv)
	if err != nil {
		t.Fatal(err)
	}

	call := engine.TaskContext{
		AgentID: "bot", ConversationID: cRow.ExecutionID.Int64,
		SourceRequestID: "user-root-2", Key: "bot/t-expire",
	}
	expInfo, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 60, Prompt: "missed check"})
	if err != nil {
		t.Fatal(err)
	}
	// Advance past 1h grace window.
	now = now.Add(2 * time.Hour)
	if err := r.service.FireDueMonitors(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetMonitor(t.Context(), strings.TrimPrefix(expInfo.ID, "schedule-"))
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "expired" || row.FireSubmissionID.String != "monitor:"+strings.TrimPrefix(expInfo.ID, "schedule-")+":1" {
		t.Fatalf("expected expired monitor, got %+v", row)
	}

	// Monitor cancels on Stop, Clear, and Archive.
	call.Key = "bot/t-stop"
	stopMonitor, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 300, Prompt: "stop me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.service.CancelSession(t.Context(), "bot", conv); err != nil {
		t.Fatal(err)
	}
	if row, err := r.q.GetMonitor(t.Context(), strings.TrimPrefix(stopMonitor.ID, "schedule-")); err != nil || row.State != "cancelled" {
		t.Fatalf("expected cancelled after Stop, got %+v, err=%v", row, err)
	}

	call.Key = "bot/t-clear"
	clearMonitor, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 300, Prompt: "clear me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.service.Clear(t.Context(), "bot", conv, "reset-monitors", "web", ""); err != nil {
		t.Fatal(err)
	}
	if row, err := r.q.GetMonitor(t.Context(), strings.TrimPrefix(clearMonitor.ID, "schedule-")); err != nil || row.State != "cancelled" {
		t.Fatalf("expected cancelled after Clear, got %+v, err=%v", row, err)
	}

	// Wait for Clear's context_reset to settle before ArchiveAgent.
	awaitReceipt(t, r, "reset-monitors")

	call.Key = "bot/t-archive"
	archMonitor, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 300, Prompt: "archive me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ArchiveAgent(t.Context(), r.q, "bot", r.service.now()); err != nil {
		t.Fatal(err)
	}
	if row, err := r.q.GetMonitor(t.Context(), strings.TrimPrefix(archMonitor.ID, "schedule-")); err != nil || row.State != "cancelled" {
		t.Fatalf("expected cancelled after ArchiveAgent, got %+v, err=%v", row, err)
	}
}

func TestUserAutomationOutboxSkipsUnchanged(t *testing.T) {
	r := startNative(t, "echo")
	conv, _ := taskParent(t, r)
	if _, err := r.q.SetTelegramSettings(t.Context(), sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "42"}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.InsertAutomationNotificationTarget(t.Context(), sqlc.InsertAutomationNotificationTargetParams{
		ID: "tgt-1", AgentID: "bot", Platform: "telegram", AccountID: "123", Peer: "telegram:42",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.SetPrimaryNotificationTarget(t.Context(), sqlc.SetPrimaryNotificationTargetParams{
		PrimaryNotificationTargetID: sql.NullString{String: "tgt-1", Valid: true}, ID: "bot",
	}); err != nil {
		t.Fatal(err)
	}
	// Create automation + unchanged run + changed run.
	if _, err := r.conn.ExecContext(t.Context(), `
		INSERT INTO automations (id, agent_id, name, prompt, schedule, time_zone, session, enabled, notification_route, created_at, updated_at)
		VALUES ('auto-1', 'bot', 'HealthCheck', 'check', '{"kind":"daily","time":"09:00"}', 'UTC', 'new', 1, 'default', 1, 1)
	`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, outcome string
		wantRows    int
	}{
		{"run-unchanged", "unchanged", 0},
		{"run-changed", "changed", 1},
	} {
		if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
			ID: tc.id, SessionID: conv, Content: "routine", Attachments: "null", Origin: "web", Producer: ProducerUser, Kind: "message", CreatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.conn.ExecContext(t.Context(), `
			INSERT INTO automation_runs (id, automation_id, status, outcome, summary, duplicate, created_at)
			VALUES (?, 'auto-1', 'started', ?, 'summary', 0, 1)
		`, tc.id, tc.outcome); err != nil {
			t.Fatal(err)
		}
		if err := queueAutomationOutbox(t.Context(), r.q, tc.id, conv, r.service.now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages WHERE id = ?`, "automation:"+tc.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != tc.wantRows {
			t.Fatalf("outcome %s: want %d outbox rows, got %d", tc.outcome, tc.wantRows, n)
		}
	}
}
