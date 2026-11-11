package chat

import (
	"errors"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func submissionCount(t *testing.T, r *nativeTest) int {
	t.Helper()
	var n int
	if err := r.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A callback that was already running when the owner archived the bot must not start new work.
func TestArchivedBotRejectsLateChannelInput(t *testing.T) {
	r := startNative(t, "echo")
	if err := db.ArchiveAgent(t.Context(), r.q, "bot", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"telegram:42", "feishu:owner", "wechat:owner"} {
		err := r.service.SubmitMessage(t.Context(), "bot", origin, "late-"+origin, Message{Content: "late input"})
		if !errors.Is(err, db.ErrAgentArchived) {
			t.Fatalf("%s: err = %v, want ErrAgentArchived", origin, err)
		}
	}
	if n := submissionCount(t, r); n != 0 {
		t.Fatalf("%d submissions exist for an archived bot", n)
	}
}

// The scheduler read a due routine, then the owner archived the idle bot. The routine did not
// change, so the snapshot check passes; the archive check must stop the run.
func TestArchivedBotRejectsSchedulerSnapshot(t *testing.T) {
	r := startNative(t, "echo")
	now := time.Now().UnixMilli()
	if _, err := r.conn.ExecContext(t.Context(), `INSERT INTO automations(id,agent_id,name,prompt,schedule,time_zone,session,enabled,next_due_at,created_at,updated_at) VALUES('auto','bot','Check','late scheduled input','{"kind":"once","at":1}','UTC','new',1,1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.q.GetAutomation(t.Context(), sqlc.GetAutomationParams{ID: "auto", AgentID: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ArchiveAgent(t.Context(), r.q, "bot", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, created, err := r.service.EnqueueAutomation(t.Context(), AutomationInput{Expected: &snapshot, AgentID: "bot", ChannelKey: "automation:auto:run", Content: snapshot.Prompt,
		Run: sqlc.CreateAutomationRunParams{ID: "run", AutomationID: snapshot.ID, Status: "started", DueAt: snapshot.NextDueAt, CreatedAt: now}})
	if !errors.Is(err, db.ErrAgentArchived) || created {
		t.Fatalf("created=%v err=%v, want ErrAgentArchived", created, err)
	}
	var runs int
	if err := r.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM automation_runs").Scan(&runs); err != nil || runs != 0 || submissionCount(t, r) != 0 {
		t.Fatalf("runs=%d submissions=%d err=%v", runs, submissionCount(t, r), err)
	}
}

// Input first orders the archive after it: the bot is busy. The archive then fails.
func TestInputBeforeArchiveMakesTheBotBusy(t *testing.T) {
	r := startNative(t, "echo")
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:42", "first", Message{Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ArchiveAgent(t.Context(), r.q, "bot", time.Now()); !errors.Is(err, db.ErrAgentBusy) {
		t.Fatalf("err = %v, want ErrAgentBusy", err)
	}
}

// A child session turn or a result is input too. The caller's run is live here, because the
// archive guard would refuse the bot; the guard is forced to prove the writers check by themselves.
func TestArchivedBotRejectsChildSessionInput(t *testing.T) {
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE agents SET archived_at = 1 WHERE id = 'bot'"); err != nil {
		t.Fatal(err)
	}
	_, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Child", Message: "work"})
	if !errors.Is(err, db.ErrAgentArchived) {
		t.Fatalf("err = %v, want ErrAgentArchived", err)
	}
}
