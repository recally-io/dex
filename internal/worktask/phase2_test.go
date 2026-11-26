package worktask_test

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worktask"
)

func TestUserTaskDependenciesWakeWhenUpstreamChanges(t *testing.T) {
	conn, q, _ := openBoardTest(t)
	ctx := t.Context()
	var wakes []int64
	svc := worktask.New(conn, func(main int64) {
		// The wake pointer must be observable from a separate read after the commit.
		_ = pendingWake(t, q, main)
		wakes = append(wakes, main)
	})
	parent := createBoardTestTask(t, svc, "Release", worktask.StatusReady, "")
	split := func(title string, dependsOn ...string) worktask.BoardSummary {
		t.Helper()
		out, err := svc.SplitTask(ctx, worktask.Owner, worktask.OriginUser, parent.ID, worktask.SplitTaskParams{
			Title: title, Status: worktask.StatusInProgress, DependsOn: dependsOn,
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
	change := func(task sqlc.WorkTask, status string) {
		t.Helper()
		if _, err := svc.TransitionStatus(ctx, worktask.Owner, worktask.OriginUser, task.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	check := func(count int, text string, reasons ...string) {
		t.Helper()
		detail, err := svc.BoardDetail(ctx, publish.Task.ID)
		if err != nil || len(detail.Blockers) != count || detail.Task.Status != worktask.StatusInProgress {
			t.Fatalf("Publish status/blockers: %+v, %v", detail, err)
		}
		if !slices.ContainsFunc(detail.Entries, func(e sqlc.WorkTaskEntry) bool {
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
			var want []worktask.TaskWakeItem
			for _, reason := range reasons {
				want = append(want, worktask.TaskWakeItem{TaskKey: worktask.FormatKey(publish.Task.Number), Reason: reason})
			}
			if !slices.Equal(items, want) {
				t.Fatalf("wake items = %+v, want %+v", items, want)
			}
		}
	}

	// When only Build is done, Publish stays blocked; cancellation wakes but does not unblock it.
	change(build.Task, worktask.StatusDone)
	check(1, "completed")
	if _, err := q.GetUnadmittedTaskWake(ctx, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("woke Publish with unfinished dependencies: %v", err)
	}
	change(verify.Task, worktask.StatusCancelled)
	check(1, "was cancelled", worktask.WakeDependencyChanged)
	change(verify.Task, worktask.StatusInProgress)
	change(verify.Task, worktask.StatusDone)
	check(0, "completed", worktask.WakeDependencyChanged, worktask.WakeGateUnblocked)

	// Then reopening Build wakes Publish again without changing its status.
	change(build.Task, worktask.StatusInProgress)
	check(1, "reopened", worktask.WakeDependencyChanged, worktask.WakeGateUnblocked)
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
		child, err := svc.SplitTask(ctx, worktask.Owner, worktask.OriginUser, parent.ID, worktask.SplitTaskParams{Title: title, DependsOn: edges}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return child.Task.ID
	}

	// Given a chain B -> A and an independent C, two edits together would form a cycle.
	a := split("A")
	b := split("B", a)
	c := split("C")
	if err := svc.SetBoardDependency(ctx, c, parent.ID, true, nil); !errors.Is(err, worktask.ErrInvalid) {
		t.Fatalf("parent is not a sibling: %v", err)
	}

	// When two tabs add A -> C and C -> B at once, exactly one edit succeeds.
	edits := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() { edits <- svc.SetBoardDependency(ctx, a, c, true, nil) })
	wg.Go(func() { edits <- svc.SetBoardDependency(ctx, c, b, true, nil) })
	wg.Wait()
	close(edits)
	var accepted, rejected int
	for err := range edits {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, worktask.ErrInvalid):
			rejected++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted %d edits and rejected %d, want one of each", accepted, rejected)
	}
}
