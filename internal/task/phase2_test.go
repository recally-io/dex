package task_test

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func TestUserTaskDependenciesWakeWhenUpstreamChanges(t *testing.T) {
	conn, q, _ := openBoardTest(t)
	ctx := t.Context()
	var wakes []int64
	svc := tasksvc.New(conn, func(main int64) {
		// The wake pointer must be observable from a separate read after the commit.
		_ = pendingWake(t, q, main)
		wakes = append(wakes, main)
	})
	parent := createBoardTestTask(t, svc, "Release", tasksvc.StatusReady, "")
	split := func(title string, dependsOn ...string) tasksvc.Summary {
		t.Helper()
		out, err := svc.SplitTask(ctx, tasksvc.Owner, tasksvc.OriginUser, parent.ID, tasksvc.SplitTaskParams{
			Title: title, Status: tasksvc.StatusInProgress, DependsOn: dependsOn,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Given Publish depends on two sibling Tasks and work on Publish has started.
	build := split("Build")
	verify := split("Verify")
	publish := split("Publish", build.Task.ID, verify.Task.ID)
	if publish.BlockerCount != 2 {
		t.Fatalf("Publish blockers = %d, want 2", publish.BlockerCount)
	}
	change := func(task sqlc.Task, status string) {
		t.Helper()
		if _, err := svc.UpdateTask(ctx, task.ID, tasksvc.UpdateParams{Status: new(status)}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(count int, text string, reasons ...string) {
		t.Helper()
		detail, err := svc.Detail(ctx, publish.Task.ID)
		if err != nil || len(detail.Blockers) != count || detail.Task.Status != tasksvc.StatusInProgress {
			t.Fatalf("Publish status/blockers: %+v, %v", detail, err)
		}
		if !slices.ContainsFunc(detail.Entries, func(e sqlc.TaskEntry) bool {
			return e.Kind == "status" && strings.HasSuffix(e.Content, text)
		}) {
			t.Fatalf("Publish has no notice ending in %q: %+v", text, detail.Entries)
		}
		if len(reasons) > 0 {
			wake, err := q.GetUnadmittedTaskWake(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			items := readWake(t, wake).Items
			var want []tasksvc.WakeItem
			for _, reason := range reasons {
				want = append(want, tasksvc.WakeItem{TaskKey: tasksvc.FormatKey(publish.Task.Number), Reason: reason})
			}
			if !slices.Equal(items, want) {
				t.Fatalf("wake items = %+v, want %+v", items, want)
			}
		}
	}

	// When only Build is done, Publish stays blocked; cancellation wakes but does not unblock it.
	change(build.Task, tasksvc.StatusDone)
	check(1, "completed")
	if _, err := q.GetUnadmittedTaskWake(ctx, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("woke Publish with unfinished dependencies: %v", err)
	}
	change(verify.Task, tasksvc.StatusCancelled)
	check(1, "was cancelled", tasksvc.WakeDependencyChanged)
	change(verify.Task, tasksvc.StatusInProgress)
	change(verify.Task, tasksvc.StatusDone)
	check(0, "completed", tasksvc.WakeDependencyChanged, tasksvc.WakeGateUnblocked)

	// Then reopening Build wakes Publish again without changing its status.
	change(build.Task, tasksvc.StatusInProgress)
	check(1, "reopened", tasksvc.WakeDependencyChanged, tasksvc.WakeGateUnblocked)
	if !slices.Equal(wakes, []int64{1, 1, 1}) {
		t.Fatalf("post-commit wake targets = %v, want [1 1 1]", wakes)
	}
}

func TestUserTaskDependenciesRejectCyclesAcrossConcurrentEdits(t *testing.T) {
	_, _, svc := openBoardTest(t)
	ctx := t.Context()
	parent := createBoardTestTask(t, svc, "Release", "", "")
	split := func(title string, edges ...string) string {
		t.Helper()
		child, err := svc.SplitTask(ctx, tasksvc.Owner, tasksvc.OriginUser, parent.ID, tasksvc.SplitTaskParams{Title: title, DependsOn: edges}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return child.Task.ID
	}

	// Given a chain B -> A and an independent C, two edits together would form a cycle.
	a := split("A")
	b := split("B", a)
	c := split("C")
	if err := svc.SetTaskDependency(ctx, c, parent.ID, true, nil); !errors.Is(err, tasksvc.ErrInvalid) {
		t.Fatalf("parent is not a sibling: %v", err)
	}

	// When two tabs add A -> C and C -> B at once, exactly one edit succeeds.
	edits := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() { edits <- svc.SetTaskDependency(ctx, a, c, true, nil) })
	wg.Go(func() { edits <- svc.SetTaskDependency(ctx, c, b, true, nil) })
	wg.Wait()
	close(edits)
	var accepted, rejected int
	for err := range edits {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, tasksvc.ErrInvalid):
			rejected++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted %d edits and rejected %d, want one of each", accepted, rejected)
	}
}
