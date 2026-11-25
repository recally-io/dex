package worktask_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/worktask"
)

func TestUserExternalLeaseAndResume(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()
	task := createBoardTestTask(t, svc, "Build", "in_progress", "")
	issue, err := svc.AttachExternalExecution(ctx, worktask.Owner, worktask.OriginUser, task.ID, worktask.ExternalParams{Harness: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := conn.QueryRowContext(ctx, "SELECT token_hash FROM task_execution_tokens WHERE execution_id = ?", issue.Execution.ID).Scan(&hash); err != nil || hash != fmt.Sprintf("%x", sha256.Sum256([]byte(issue.BoardToken))) {
		t.Fatalf("token digest did not match: %v", err)
	}
	boardExec(t, conn, "UPDATE task_executions SET lease_expires_at = 1 WHERE id = ?", issue.Execution.ID)
	if _, err := svc.ExternalHeartbeat(ctx, issue.BoardToken, worktask.ExternalHeartbeat{}); !errors.Is(err, worktask.ErrClaimLost) {
		t.Fatalf("expired heartbeat: %v", err)
	}
	now := time.Now()
	if err := svc.ExtendActiveLeasesOnStartup(ctx, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	e, err := q.GetExternalExecution(ctx, issue.Execution.ID)
	if err != nil || e.LeaseExpiresAt.Int64 != now.Add(time.Minute).UnixMilli() {
		t.Fatalf("startup lease: %+v %v", e, err)
	}
	if err := svc.ExtendActiveLeasesOnStartup(ctx, now, time.Second); err != nil {
		t.Fatal(err)
	}
	e, err = q.GetExternalExecution(ctx, issue.Execution.ID)
	if err != nil || e.LeaseExpiresAt.Int64 != now.Add(time.Minute).UnixMilli() {
		t.Fatalf("startup shortened lease: %+v %v", e, err)
	}
	if err := svc.ExpireExternalLeases(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e, err = q.GetExternalExecution(ctx, issue.Execution.ID)
	if err != nil || e.State != "stale" {
		t.Fatalf("expired execution: %+v %v", e, err)
	}
	resumed, err := svc.ResumeExternalExecution(ctx, worktask.Owner, worktask.OriginUser, e.ID, worktask.ExternalParams{})
	if err != nil || resumed.Execution.State != "running" || resumed.Execution.LeaseEpoch != 2 {
		t.Fatalf("resume state/epoch: %s/%d: %v", resumed.Execution.State, resumed.Execution.LeaseEpoch, err)
	}
	if _, err := svc.ExternalPostEntry(ctx, issue.BoardToken, worktask.PostEntryParams{Content: "old"}); !errors.Is(err, worktask.ErrClaimLost) {
		t.Fatalf("superseded post: %v", err)
	}
	if _, err := svc.ExternalFinish(ctx, resumed.BoardToken, "failed", "could not build"); err != nil {
		t.Fatal(err)
	}
	pending, err := q.GetUnadmittedTaskWake(ctx, 1)
	if err != nil || pending.Content != `{"highWaterEntryId":5,"items":[{"taskKey":"T-1","reason":"execution_stale"},{"taskKey":"T-1","reason":"execution_failed"}]}` {
		t.Fatalf("coalesced Main wake: %s %v", pending.Content, err)
	}
}

func TestUserResumesManagedExternalRunOnlyOnItsOriginalEnrollment(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()
	task := createBoardTestTask(t, svc, "Build on a Runner", "in_progress", "")
	other := createBoardTestTask(t, svc, "Unrelated Task", "in_progress", "")
	boardExec(t, conn, `INSERT INTO execution_environments (id,name,kind,config,created_at,updated_at) VALUES ('runner','Remote','runner','{}',1,1)`)
	manager := runner.NewManager(runner.ManagerConfig{DataDir: t.TempDir(), Queries: q})
	defer manager.Close(ctx)
	machines := manager.Runners()
	enroll := func() {
		t.Helper()
		code, err := machines.IssueCode(ctx, "runner")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := machines.Enroll(ctx, "runner", code); err != nil {
			t.Fatal(err)
		}
	}
	enroll()
	p := worktask.ExternalParams{Harness: "custom", MachineID: "runner", WorkspacePath: "/project", ExternalSessionID: "harness-session", DispatchID: "dispatch-first"}
	call := func(taskID, executionID string, p worktask.ExternalParams) (worktask.ExternalIssue, error) {
		return svc.DispatchExternalExecution(ctx, worktask.Bot("assignee"), worktask.OriginUser, taskID, executionID, p)
	}
	first, err := call(task.ID, "", p)
	if err != nil {
		t.Fatal(err)
	}
	resume := p
	resume.DispatchID = "dispatch-resume"
	wrong := resume
	wrong.WorkspacePath = "/other"
	if _, err := call(task.ID, first.Execution.ID, wrong); !errors.Is(err, worktask.ErrConflict) {
		t.Fatalf("other workspace: %v", err)
	}
	if _, err := call(other.ID, first.Execution.ID, resume); !errors.Is(err, worktask.ErrConflict) {
		t.Fatalf("other Task: %v", err)
	}
	if _, err := call(task.ID, first.Execution.ID, resume); err != nil {
		t.Fatal(err)
	}
	if _, err := call(task.ID, "", p); !errors.Is(err, worktask.ErrDispatchExists) {
		t.Fatalf("superseded dispatch replay: %v", err)
	}
	if err := machines.Revoke(ctx, "runner"); err != nil {
		t.Fatal(err)
	}
	enroll()
	resume.DispatchID = "dispatch-after-enrollment"
	if _, err := call(task.ID, first.Execution.ID, resume); !errors.Is(err, worktask.ErrConflict) {
		t.Fatalf("other enrollment on same Runner ID: %v", err)
	}
}
