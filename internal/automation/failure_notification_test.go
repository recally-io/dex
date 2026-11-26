package automation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/automation"

	"github.com/recally-io/dex/internal/chat"
)

func TestUserRoutineScheduleAndRunnerFailuresAlertButBusyDoesNot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		alert        bool
	}{
		{"missed schedule", chat.RoutineSkipMissed, true},
		{"unavailable Runner", "the Runner Box is unavailable", true},
		{"busy overlap", chat.RoutineSkipBusy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a Routine, an existing conversation and a configured same-bot destination.
			f := newRoutineFixture(t)
			ctx := t.Context()
			if _, err := f.chats.Main(ctx, "a"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.conn.ExecContext(ctx, `INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('a', '123:fixture', '42');
INSERT INTO automation_notification_targets (id,agent_id,platform,account_id,peer) VALUES ('primary','a','telegram','123','telegram:42'); UPDATE agents SET primary_notification_target_id='primary' WHERE id='a'`); err != nil {
				t.Fatal(err)
			}
			in := routineInput()
			in.Notification = &automation.Notification{Mode: "default"}
			if tc.name == "missed schedule" {
				in.Schedule.Kind = "daily"
				in.Schedule.Time, in.Schedule.Minutes = "09:00", 0
			}
			if tc.name == "unavailable Runner" {
				if _, err := f.conn.ExecContext(ctx, `INSERT INTO runners (id,name,kind,config,secret,created_at,updated_at) VALUES ('box','Box','pi-env','{"host":"fixture.invalid"}','',0,0)`); err != nil {
					t.Fatal(err)
				}
				in.RunnerID = "box"
				f.routines.ConfigureProbe(func(context.Context, string, string) error { return errors.New("fixture private diagnostic") })
			}
			a, err := f.routines.Create(ctx, "a", in)
			if err != nil {
				t.Fatal(err)
			}
			// When the run encounters the pre-start condition.
			switch tc.name {
			case "missed schedule":
				due := a.NextRuns[0]
				f.now = time.UnixMilli(due).Add(2 * time.Hour)
				f.routines.Check(ctx)
				// Replay the same due time (as if Dex stopped after the run commit but before cursor advance).
				if _, err = f.conn.ExecContext(ctx, `UPDATE automations SET next_due_at = ? WHERE id = ?`, due, a.ID); err != nil {
					t.Fatal(err)
				}
				f.routines.Check(ctx)
			case "unavailable Runner":
				if _, err = f.routines.RunNow(ctx, "a", a.ID); err != nil {
					t.Fatal(err)
				}
			case "busy overlap":
				if _, err = f.routines.RunNow(ctx, "a", a.ID); err != nil {
					t.Fatal(err)
				}
				f.now = f.now.Add(time.Millisecond)
				if _, err = f.routines.RunNow(ctx, "a", a.ID); err != nil {
					t.Fatal(err)
				}
			}
			// Then the run is stored, without raw probe errors, and only a failure skip queues an alert.
			runs, err := f.routines.Runs(ctx, "a", a.ID, 10)
			wantRuns := 1
			if !tc.alert {
				wantRuns = 2
			}
			if err != nil || len(runs) != wantRuns || runs[0].Status != "skipped" || runs[0].Reason != tc.reason || runs[0].NotificationNote != "" || runs[0].ChatID != 0 {
				t.Fatalf("runs=%+v, err=%v", runs, err)
			}
			var count int
			var body string
			if err = f.conn.QueryRowContext(ctx, `SELECT count(*), coalesce(max(body),'') FROM outbox_messages WHERE kind = 'automation'`).Scan(&count, &body); err != nil {
				t.Fatal(err)
			}
			if tc.alert && (count != 1 || body != "routine: failed\n"+tc.reason) || !tc.alert && count != 0 {
				t.Fatalf("alerts=%d, body=%q", count, body)
			}
		})
	}
}

func TestUserRoutineDoesNotCreateConversationToOwnAnAlert(t *testing.T) {
	t.Parallel()
	// Given a bot with an authorized destination but no conversation.
	f := newRoutineFixture(t)
	ctx := t.Context()
	if _, err := f.conn.ExecContext(ctx, `INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('a', '123:fixture', '42');
INSERT INTO automation_notification_targets (id,agent_id,platform,account_id,peer) VALUES ('primary','a','telegram','123','telegram:42'); UPDATE agents SET primary_notification_target_id='primary', model_id='' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	in := routineInput()
	in.Notification = &automation.Notification{Mode: "default"}
	a, err := f.routines.Create(ctx, "a", in)
	if err != nil {
		t.Fatal(err)
	}
	// When the routine cannot start, the run remains visible but has no Outbox row or chat.
	if _, err = f.routines.RunNow(ctx, "a", a.ID); err != nil {
		t.Fatal(err)
	}
	runs, err := f.routines.Runs(ctx, "a", a.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].NotificationNote != "not delivered: no existing bot conversation" {
		t.Fatalf("runs=%+v, err=%v", runs, err)
	}
	var conversations, alerts int
	if err = f.conn.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM conversations), (SELECT count(*) FROM outbox_messages)`).Scan(&conversations, &alerts); err != nil || conversations != 0 || alerts != 0 {
		t.Fatalf("conversations=%d, alerts=%d, err=%v", conversations, alerts, err)
	}
}
