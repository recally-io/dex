package automation_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/execenv"
)

type routineFixture struct {
	q        *sqlc.Queries
	conn     *sql.DB
	chats    *chat.Service
	routines *automation.Service
	now      time.Time
}

func newRoutineFixture(t *testing.T) *routineFixture {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err = conn.ExecContext(t.Context(), `INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('a','a','faux','echo',0)`); err != nil {
		t.Fatal(err)
	}
	native, err := engine.New(engine.Config{DataDir: dir, Home: t.TempDir(), BaseEnv: map[string]string{}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := native.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	f := &routineFixture{conn: conn, q: sqlc.New(conn), now: time.Date(2016, 1, 1, 0, 0, 0, 123000000, time.UTC)}
	clock := func() time.Time { return f.now }
	f.chats = chat.New(conn, native, clock, nil, nil)
	f.routines = automation.New(f.q, f.chats, clock)
	return f
}

func routineInput() automation.Input {
	return automation.Input{Name: "routine", Prompt: "old prompt", Schedule: automation.Schedule{Kind: "every", Minutes: 5}, TimeZone: "UTC", Session: "new", Enabled: true}
}

func TestUserScheduledRoutineDoesNotRunStaleInput(t *testing.T) {
	for _, change := range []string{"prompt", "pause", "delete"} {
		t.Run(change, func(t *testing.T) {
			f := newRoutineFixture(t)
			ctx := t.Context()
			in := routineInput()
			a, err := f.routines.Create(ctx, "a", in)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.q.GetAutomation(ctx, sqlc.GetAutomationParams{ID: a.ID, AgentID: "a"})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "prompt":
				in.Prompt = "new prompt"
				_, err = f.routines.Update(ctx, "a", a.ID, in)
			case "pause":
				in.Enabled = false
				_, err = f.routines.Update(ctx, "a", a.ID, in)
			case "delete":
				err = f.routines.Delete(ctx, "a", a.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Submit the previously captured snapshot after the user change has committed.
			id := uuid.NewV7().String()
			_, created, enqueueErr := f.chats.EnqueueAutomation(ctx, chat.AutomationInput{
				Expected: &snapshot, AgentID: "a", ChannelKey: "automation:" + a.ID + ":" + id, Title: snapshot.Name, Content: snapshot.Prompt, Cwd: snapshot.Cwd,
				Run: sqlc.CreateAutomationRunParams{ID: id, AutomationID: a.ID, DueAt: snapshot.NextDueAt, Status: "started", CreatedAt: f.now.UnixMilli()},
			})
			var count int
			if err = f.conn.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE origin='automation' AND content='old prompt'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 || created {
				t.Fatalf("stale input committed after %s: count=%d created=%v", change, count, created)
			}
			if !errors.Is(enqueueErr, chat.ErrAutomationChanged) {
				t.Fatalf("enqueue after %s: %v", change, enqueueErr)
			}
			if change == "delete" {
				if _, err = f.routines.Get(ctx, "a", a.ID); !errors.Is(err, automation.ErrNotFound) {
					t.Fatalf("deleted routine: %v", err)
				}
				return
			}
			saved, err := f.routines.Get(ctx, "a", a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Prompt != in.Prompt || saved.Enabled != in.Enabled {
				t.Fatalf("saved routine: %+v", saved)
			}
			if change == "prompt" && (len(saved.NextRuns) == 0 || saved.NextRuns[0] != a.NextRuns[0]) {
				t.Fatalf("saved cursor: %v", saved.NextRuns)
			}
			runs, err := f.routines.Runs(ctx, "a", a.ID, 10)
			if err != nil || len(runs) != 0 {
				t.Fatalf("runs after %s: %v, %v", change, runs, err)
			}
		})
	}
}

func TestUserIntervalCatchUpKeepsScheduleAlignment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		run     bool
		latest  time.Duration
	}{
		{"before due", -time.Millisecond, false, 0},
		{"at due", 0, true, 0},
		{"before next", 5*time.Minute - time.Millisecond, true, 0},
		{"at next", 5 * time.Minute, true, 5 * time.Minute},
		{"after next", 5*time.Minute + time.Millisecond, true, 5 * time.Minute},
		{"ten years", 10*365*24*time.Hour + 7*time.Minute, true, 10*365*24*time.Hour + 5*time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRoutineFixture(t)
			ctx := t.Context()
			a, err := f.routines.Create(ctx, "a", routineInput())
			if err != nil {
				t.Fatal(err)
			}
			due := time.UnixMilli(a.NextRuns[0])
			f.now = due.Add(tc.elapsed)
			f.routines.Check(ctx)
			runs, err := f.routines.Runs(ctx, "a", a.ID, 10)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.run {
				if len(runs) != 0 {
					t.Fatalf("early runs: %+v", runs)
				}
			} else if len(runs) != 1 || runs[0].DueAt != due.Add(tc.latest).UnixMilli() || runs[0].Status != "running" {
				t.Fatalf("catch-up runs: %+v", runs)
			}
			saved, err := f.routines.Get(ctx, "a", a.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantNext := due
			if tc.run {
				wantNext = due.Add(tc.latest + 5*time.Minute)
			}
			if len(saved.NextRuns) == 0 || saved.NextRuns[0] != wantNext.UnixMilli() {
				t.Fatalf("next times: %v, want %v", saved.NextRuns, wantNext)
			}
		})
	}
}

// Given a routine that follows a bot on an SSH environment, when the bot moves to another
// environment during the reachability check, then no run is stored for the environment that was
// not checked, and the caller is told to run it again.
func TestUserRoutineRunsOnlyOnTheCheckedEnvironment(t *testing.T) {
	f := newRoutineFixture(t)
	ctx := t.Context()
	if _, err := f.conn.ExecContext(ctx, `INSERT INTO execution_environments (id,name,kind,config,secret,created_at,updated_at) VALUES ('box','box','pi-env','{"host":"box.internal"}','',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.ExecContext(ctx, `UPDATE agents SET environment_id = 'box' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	a, err := f.routines.Create(ctx, "a", routineInput())
	if err != nil {
		t.Fatal(err)
	}
	var probed []string
	f.routines.ConfigureProbe(func(ctx context.Context, agentID, environmentID string) error {
		probed = append(probed, environmentID)
		_, err := f.conn.ExecContext(ctx, `UPDATE agents SET environment_id = '' WHERE id = ?`, agentID)
		return err
	})
	if _, err := f.routines.RunNow(ctx, "a", a.ID); !errors.Is(err, engine.ErrBusy) {
		t.Fatalf("run after the bot moved: err=%v", err)
	}
	runs, err := f.routines.Runs(ctx, "a", a.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 || len(probed) != 1 || probed[0] != "box" {
		t.Fatalf("runs=%+v probed=%v", runs, probed)
	}
}

// Given a routine on an SSH environment, when the owner edits it during the reachability check of
// Run Now, then no run starts with the old settings.
func TestUserRoutineEditDuringRunNowCheckStartsNoRun(t *testing.T) {
	f := newRoutineFixture(t)
	ctx := t.Context()
	if _, err := f.conn.ExecContext(ctx, `INSERT INTO execution_environments (id,name,kind,config,secret,created_at,updated_at) VALUES ('box','box','pi-env','{"host":"box.internal"}','',0,0)`); err != nil {
		t.Fatal(err)
	}
	in := routineInput()
	in.EnvironmentID = "box"
	a, err := f.routines.Create(ctx, "a", in)
	if err != nil {
		t.Fatal(err)
	}
	f.routines.ConfigureProbe(func(ctx context.Context, agentID, _ string) error {
		in.EnvironmentID = execenv.LocalID
		_, err := f.routines.Update(ctx, agentID, a.ID, in)
		return err
	})
	if run, err := f.routines.RunNow(ctx, "a", a.ID); !errors.Is(err, engine.ErrBusy) {
		t.Fatalf("run after an edit during its check: run=%+v err=%v", run, err)
	}
	runs, err := f.routines.Runs(ctx, "a", a.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs=%+v", runs)
	}
}
