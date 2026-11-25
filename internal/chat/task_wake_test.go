package chat

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/worktask"
)

func TestUserBoardWakeCoalescesWhileMainIsBusy(t *testing.T) {
	r := startNative(t, "bash")
	ctx := t.Context()
	started, release := filepath.Join(r.dir, "hold.started"), filepath.Join(r.dir, "hold.release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	hold := fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.05; done; echo hold", started, release)
	main, err := r.service.Post(ctx, "bot", MainKey, "", "busy-main", hold)
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, started)

	// Five real source mutations share the same pending slot while Main runs.
	tasks := worktask.New(r.conn, nil)
	var arrivals sync.WaitGroup
	for i := range 5 {
		arrivals.Go(func() {
			if _, err := tasks.AcceptCreating(ctx, wakeTestEvent(i), "bot", fmt.Sprintf("Task %d", i)); err != nil {
				t.Errorf("source event %d: %v", i, err)
			}
		})
	}
	arrivals.Wait()
	wake, err := r.q.GetUnadmittedTaskWake(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	var payload worktask.TaskWakePayload
	if err := json.Unmarshal([]byte(wake.Content), &payload); err != nil || len(payload.Items) != 5 || strings.Contains(wake.Content, "secret body") {
		t.Fatalf("wake payload = %s, %v; want five pointers and no body text", wake.Content, err)
	}
	var cursor int64
	if err := r.conn.QueryRowContext(ctx, `SELECT max(id) FROM work_task_entries`).Scan(&cursor); err != nil || cursor != payload.HighWaterEntryID {
		t.Fatalf("ledger cursor = %d, %v; want %d", cursor, err, payload.HighWaterEntryID)
	}
	if err := r.service.reconcileSession(ctx, main, ""); !errors.Is(err, engine.ErrBusy) {
		t.Fatalf("wake attempt during active turn: %v; want busy", err)
	}
	if _, err := r.service.Post(ctx, "bot", MainKey, "", "owner-next", "echo owner-first"); err != nil {
		t.Fatal(err)
	}
	if !receipt(t, r, "owner-next").AdmittedAt.Valid || receipt(t, r, wake.ID).AdmittedAt.Valid {
		t.Fatal("owner input did not pass the unadmitted Board wake")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if user := awaitReceipt(t, r, "owner-next"); user.Status != "done" || user.Answer.String != "bash: owner-first" {
		t.Fatalf("owner receipt: %+v", user)
	}
	waitFor(t, func() bool {
		err := r.service.reconcileSession(ctx, main, "")
		if err != nil && !errors.Is(err, engine.ErrBusy) {
			t.Fatal(err)
		}
		return err == nil
	})
	if saved := awaitReceipt(t, r, wake.ID); !saved.AdmittedAt.Valid || saved.Content != wake.Content {
		t.Fatalf("Board wake receipt: %+v", saved)
	}
	assertWakeCounts(t, r, 1)
}

func TestUserBoardWakeReplaysAdmissionAndKeepsLatePointers(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	tasks := worktask.New(r.conn, nil)
	create := func(i int) {
		t.Helper()
		if _, err := tasks.AcceptCreating(t.Context(), wakeTestEvent(i), "bot", fmt.Sprintf("Task %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	create(1)
	conv, err := r.q.GetConversationByKey(ctx, sqlc.GetConversationByKeyParams{AgentID: "bot", ChannelKey: MainKey})
	if err != nil {
		t.Fatal(err)
	}
	wake, err := r.q.GetUnadmittedTaskWake(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	options := engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"}
	ref, err := r.service.execution(ctx, conv.ID, "bot", options)
	if err != nil {
		t.Fatal(err)
	}
	// The second event arrives after the first receipt read but before the native commit.
	create(2)
	before, err := r.q.GetUnadmittedTaskWake(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	native, err := r.agents.Submit(ctx, ref, engine.Input{
		RequestID: wake.ID, Content: wake.Content, Kind: "task_wake", Origin: "web", Main: true, TaskTitle: "Board wake", Options: options,
		RefreshContent: func(ctx context.Context) (string, error) { return r.service.snapshotTaskWake(ctx, wake.ID) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := native.Wait(ctx); err != nil || !strings.Contains(out.Answer, `"taskKey":"T-2"`) {
		t.Fatalf("native run did not see the refreshed wake: %+v, %v", out, err)
	}
	// A third event arrives after native admission but before the receipt update. Restart there.
	create(3)
	latest, err := r.q.GetUnadmittedTaskWake(ctx, conv.ID)
	if err != nil || latest.ID != wake.ID || latest.Content == before.Content {
		t.Fatalf("late event did not update the unconfirmed wake: %+v, %v", latest, err)
	}
	r.close()
	reopened := openNativeTest(t, r.dir, "echo")
	if err := reopened.service.reconcileSession(ctx, conv.ID, ""); err != nil {
		t.Fatal(err)
	}
	if restored := awaitReceipt(t, reopened, wake.ID); !restored.AdmittedAt.Valid || restored.Content != before.Content {
		t.Fatalf("old wake must match its original native snapshot: %+v", restored)
	}
	next, err := reopened.q.GetUnadmittedTaskWake(ctx, conv.ID)
	if err != nil || next.ID == wake.ID || next.Content != latest.Content {
		t.Fatalf("pending wake after restart: %+v, %v; want fresh ID with latest pointers", next, err)
	}
	if err := reopened.service.reconcileSession(ctx, conv.ID, ""); err != nil {
		t.Fatal(err)
	}
	if second := awaitReceipt(t, reopened, next.ID); second.Status != "done" || !strings.Contains(second.Answer.String, `"taskKey":"T-3"`) {
		t.Fatalf("new wake did not deliver the late event: %+v", second)
	}
	assertWakeCounts(t, reopened, 2)
}

func wakeTestEvent(i int) worktask.Event {
	key := fmt.Sprintf("issue-%d", i)
	return worktask.Event{Namespace: "test", ReceiptKey: key, Type: "opened", Content: "secret body", OccurredAt: 1,
		Objects: []worktask.Object{{Type: "issue", Key: key}}}
}

func assertWakeCounts(t *testing.T, r *nativeTest, want int) {
	t.Helper()
	var wakes, replies int
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE kind = 'task_wake'`).Scan(&wakes); err != nil || wakes != want {
		t.Fatalf("wake count = %d, %v; want %d", wakes, err, want)
	}
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages`).Scan(&replies); err != nil || replies != 0 {
		t.Fatalf("unexpected channel messages: %d, %v", replies, err)
	}
}
