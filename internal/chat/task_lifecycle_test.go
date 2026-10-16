package chat_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
	"github.com/recally-io/dex/internal/workerbin"
)

type taskRuntime struct {
	service      *chat.Service
	worker       *worker.Worker
	q            *sqlc.Queries
	dir          string
	parent       int64
	workerParent int64
}

type taskCaller struct {
	*worker.Worker
	onBind func(error)
}

func (c taskCaller) Bind() (worker.CallFunc, error) {
	call, err := c.Worker.Bind()
	if c.onBind != nil {
		c.onBind(err)
	}
	return call, err
}

func startTaskRuntime(t *testing.T, beforeInit func(context.Context), onTask func(context.Context, worker.TaskRequest, *chat.Service) (any, error), onBind func(error), onSettled ...func(context.Context, worker.Settled)) *taskRuntime {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "bot", Name: "Dex"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", ModelProvider: "faux", ModelID: "spawn", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	path, err := worker.Install(t.TempDir(), workerbin.Gzip)
	if err != nil {
		t.Fatal(err)
	}
	var service *chat.Service
	w := worker.New(worker.Config{
		Path: path, Dir: dir,
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "DEX_DATA_DIR=" + dir, "DEX_FAKE_MODEL=1"},
		OnSettled: func(ctx context.Context, event worker.Settled) {
			if len(onSettled) > 0 {
				onSettled[0](ctx, event)
				return
			}
			service.HandleSettled(ctx, event)
		},
		OnTask: func(ctx context.Context, request worker.TaskRequest) (any, error) {
			if onTask != nil {
				return onTask(ctx, request, service)
			}
			return service.HandleTask(ctx, request)
		},
		Init: func(ctx context.Context, call worker.CallFunc) error {
			if beforeInit != nil {
				beforeInit(ctx)
			}
			return service.RecoverTasks(ctx, call)
		},
	})
	service = chat.New(conn, taskCaller{Worker: w, onBind: onBind}, time.Now, nil, nil)
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	var parent worker.CreateConversationResult
	if err := w.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "bot", Model: worker.ModelRef{Provider: "faux", ModelID: "spawn"}, ThinkingLevel: "off"}, &parent); err != nil {
		t.Fatal(err)
	}
	id, err := service.Post(t.Context(), "bot", "web:lifecycle", "Lifecycle", "source", "seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetWorkerConversation(t.Context(), sqlc.SetWorkerConversationParams{ID: id, WorkerConversationID: sql.NullInt64{Int64: parent.ConversationID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	return &taskRuntime{service: service, worker: w, q: q, dir: dir, parent: id, workerParent: parent.ConversationID}
}

func (r *taskRuntime) spawnRequest(t *testing.T, key string, args any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return r.service.HandleTask(t.Context(), worker.TaskRequest{AgentID: "bot", ConversationID: r.workerParent, SourceRequestID: "source", Key: "bot/" + key, Action: "spawn", Args: raw})
}

func (r *taskRuntime) spawn(t *testing.T, key string) (chat.Task, string) {
	t.Helper()
	result, err := r.spawnRequest(t, key, map[string]string{"title": key, "goal": "bounded work", "provider": "faux", "modelId": "echo"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	task, err := r.service.GetTask(t.Context(), "bot", r.parent, reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task, reply.ID
}

func TestUserDoesNotReceiveTasksFromAnExitedWorker(t *testing.T) {
	// Given a real task RPC paused before control accepts its write.
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	callbackDone := make(chan error, 1)
	r := startTaskRuntime(t, nil, func(ctx context.Context, request worker.TaskRequest, service *chat.Service) (any, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		result, err := service.HandleTask(ctx, request)
		callbackDone <- err
		return result, err
	}, nil)
	if err := r.worker.Call(t.Context(), "submit", worker.SubmitParams{AgentID: "bot", ConversationID: r.workerParent,
		Model: worker.ModelRef{Provider: "faux", ModelID: "spawn"}, ThinkingLevel: "off", RequestID: "source",
		Content: `{"title":"Late task","goal":"Must not start","provider":"faux","modelId":"echo"}`,
		Source:  "dex:chat/1", Attachments: []worker.Attachment{}}, &worker.SubmitResult{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("task RPC did not arrive")
	}
	// When the worker exits, its pending callback must end before restart returns.
	restarted := make(chan error, 1)
	go func() { restarted <- r.worker.Restart(t.Context()) }()
	select {
	case err := <-callbackDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exited worker callback: %v", err)
		}
	case err := <-restarted:
		if err != nil {
			t.Fatal(err)
		}
		releaseOnce()
		if err := <-callbackDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("exited worker created work: %v", err)
		}
		tasks, err := r.service.ListTasks(t.Context(), "bot", r.parent)
		if err != nil || len(tasks) != 0 {
			t.Fatalf("tasks after worker exit: %+v, %v", tasks, err)
		}
		return
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not cancel its old task callback")
	}
	if err := <-restarted; err != nil {
		t.Fatal(err)
	}
	// Then no goal was created after the old process ended.
	tasks, err := r.service.ListTasks(t.Context(), "bot", r.parent)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after worker exit: %+v, %v", tasks, err)
	}
}

func TestUserQueuedTaskSurvivesWorkerRestart(t *testing.T) {
	// Given a queued execution with an empty, saved worker conversation.
	r := startTaskRuntime(t, nil, nil, nil)
	goal, stageID := r.spawn(t, "Queued")
	if _, err := r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	stage, err := r.q.GetTask(t.Context(), stageID)
	if err != nil {
		t.Fatal(err)
	}
	var created worker.CreateConversationResult
	if err := r.worker.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "bot", Model: worker.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"}, &created); err != nil {
		t.Fatal(err)
	}
	if err := r.q.SetWorkerConversation(t.Context(), sqlc.SetWorkerConversationParams{ID: stage.ConversationID, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	// When the worker restarts before the execution starts, control can deliver its first input.
	if err := r.worker.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.service.Run(ctx) }()
	t.Cleanup(func() { stop(); <-done })
	// Then the successful report releases its slot instead of becoming interrupted.
	taskEventually(t, func() (bool, any) {
		got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "interrupted" {
			t.Fatalf("never-started task became interrupted: %+v", got)
		}
		return got.Status == "done" && got.Report == "echo: bounded work", got
	})
}

func TestUserCancelsQueuedTaskAcrossWorkerRestart(t *testing.T) {
	for _, savedConversation := range []bool{false, true} {
		name := "without worker conversation"
		if savedConversation {
			name = "with empty worker conversation"
		}
		t.Run(name, func(t *testing.T) {
			// Given a shell execution that has not been admitted to the worker.
			r := startTaskRuntime(t, nil, nil, nil)
			marker := filepath.Join(r.dir, "must-not-run")
			if _, err := r.spawnRequest(t, "cancel-before-start", map[string]string{
				"title": "Cancel before start", "goal": "printf 'unexpected execution\\n' > '" + marker + "'", "provider": "faux", "modelId": "bash",
			}); err != nil {
				t.Fatal(err)
			}
			tasks, err := r.service.ListTasks(t.Context(), "bot", r.parent)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("queued task: %+v, %v", tasks, err)
			}
			goal, stageID := tasks[0], tasks[0].ID
			stage, err := r.q.GetTask(t.Context(), stageID)
			if err != nil || stage.StartedAt.Valid {
				t.Fatalf("execution already started: %+v, %v", stage, err)
			}
			if savedConversation {
				var created worker.CreateConversationResult
				if err := r.worker.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "bot", Model: worker.ModelRef{Provider: "faux", ModelID: "bash"}, ThinkingLevel: "off"}, &created); err != nil {
					t.Fatal(err)
				}
				if err := r.q.SetWorkerConversation(t.Context(), sqlc.SetWorkerConversationParams{ID: stage.ConversationID, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "done"}); err != nil {
				t.Fatal(err)
			}
			// When cancellation is accepted, the worker restarts before cancellation settles.
			if _, err := r.service.CancelTask(t.Context(), "bot", r.parent, goal.ID); err != nil {
				t.Fatal(err)
			}
			if err := r.worker.Restart(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); r.service.Run(ctx) }()
			stopService := sync.OnceFunc(func() { stop(); <-done })
			t.Cleanup(stopService)
			// Then the task is cancelled, with no tool effects and both slots available.
			taskEventually(t, func() (bool, any) {
				got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status == "interrupted" {
					t.Fatalf("never-started cancellation became interrupted: %+v", got)
				}
				return got.Status == "cancelled", got
			})
			stopService()
			got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
			if err != nil || got.StartedAt != nil {
				t.Fatalf("cancelled execution started: %+v, %v", got, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled execution left a tool artifact: %v", err)
			}
			if _, err := r.service.Post(t.Context(), "bot", "web:lifecycle", "Lifecycle", "next-source", "new work"); err != nil {
				t.Fatal(err)
			}
			spawn := func(key string) error {
				t.Helper()
				args, err := json.Marshal(map[string]string{"title": key, "goal": "bounded work", "provider": "faux", "modelId": "echo"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = r.service.HandleTask(t.Context(), worker.TaskRequest{AgentID: "bot", ConversationID: r.workerParent, SourceRequestID: "next-source", Key: "bot/" + key, Action: "spawn", Args: args})
				return err
			}
			for _, key := range []string{"next-first", "next-second"} {
				if err := spawn(key); err != nil {
					t.Fatalf("slot was not released: %v", err)
				}
			}
		})
	}
}

func taskEventually(t *testing.T, condition func() (bool, any)) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		ok, last := condition()
		if ok {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("task condition did not complete: %+v", last)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestUserTaskWaitsWithoutAdmissionDuringWorkerRecovery(t *testing.T) {
	// Given a queued execution whose empty worker conversation is already saved.
	initializing, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	var starts int
	bound := make(chan error, 1)
	r := startTaskRuntime(t, func(ctx context.Context) {
		starts++
		if starts == 2 {
			close(initializing)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}, nil, func(err error) {
		select {
		case bound <- err:
		default:
		}
	})
	t.Cleanup(releaseOnce)
	goal, stageID := r.spawn(t, "Recovery window")
	if _, err := r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	stage, err := r.q.GetTask(t.Context(), stageID)
	if err != nil {
		t.Fatal(err)
	}
	var created worker.CreateConversationResult
	if err := r.worker.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "bot", Model: worker.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"}, &created); err != nil {
		t.Fatal(err)
	}
	if err := r.q.SetWorkerConversation(t.Context(), sqlc.SetWorkerConversationParams{ID: stage.ConversationID, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan error, 1)
	go func() { restarted <- r.worker.Restart(t.Context()) }()
	select {
	case <-initializing:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not reach recovery")
	}
	// When control attempts delivery during recovery, it must retain the queued state.
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.service.Run(ctx) }()
	t.Cleanup(func() { stop(); <-done })
	t.Cleanup(releaseOnce)
	// Observe the real admission result while Init is held at the process boundary.
	select {
	case err := <-bound:
		if !errors.Is(err, worker.ErrUnavailable) {
			t.Fatalf("recovery admission: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execution dispatch waited for the next worker instead of retaining queued state")
	}
	got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
	if err != nil || got.Status != "queued" {
		t.Fatalf("recovery queued state: %+v, %v", got, err)
	}
	releaseOnce()
	if err := <-restarted; err != nil {
		t.Fatal(err)
	}
	// Then the same queued execution completes once on the ready worker.
	taskEventually(t, func() (bool, any) {
		got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "interrupted" {
			t.Fatalf("recovery admitted uncertain execution: %+v", got)
		}
		return got.Status == "done", got
	})
}

func (r *taskRuntime) executeQueued(t *testing.T, id string) {
	t.Helper()
	row, err := r.q.GetTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var created worker.CreateConversationResult
	if err := r.worker.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "bot", Model: worker.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"}, &created); err != nil {
		t.Fatal(err)
	}
	if err := r.q.SetWorkerConversation(t.Context(), sqlc.SetWorkerConversationParams{ID: row.ConversationID, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.StartTask(t.Context(), sqlc.StartTaskParams{ID: id, Now: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := r.worker.Call(t.Context(), "submit", worker.SubmitParams{AgentID: "bot", ConversationID: created.ConversationID, TaskStageID: id, Model: worker.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off", RequestID: id, Content: "bounded work", Attachments: []worker.Attachment{}}, &worker.SubmitResult{}); err != nil {
		t.Fatal(err)
	}
	taskEventually(t, func() (bool, any) {
		got, err := r.service.GetTask(t.Context(), "bot", r.parent, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "cancelling" {
			// This fixture has no delivery loop. Get cleanup evidence from the real worker.
			var stopped worker.AbortResult
			if err := r.worker.Call(t.Context(), "abort", worker.AbortParams{AgentID: "bot", ConversationID: created.ConversationID, TaskStageID: id}, &stopped); err != nil {
				t.Fatal(err)
			}
			if !stopped.ExecutionConfirmed {
				t.Fatalf("worker did not confirm cleanup: %+v", stopped)
			}
			submission, err := r.q.GetSubmission(t.Context(), id)
			if err != nil || submission.Status != "done" {
				t.Fatalf("natural settlement: %+v err=%v", submission, err)
			}
			r.service.HandleSettled(t.Context(), worker.Settled{AgentID: "bot", ConversationID: created.ConversationID, RequestID: id, Status: "done", Answer: submission.Answer.String, ExecutionConfirmed: stopped.ExecutionConfirmed, StopNote: stopped.StopNote})
		}
		return got.Status == "done", got
	})
}

func TestUserDelegationReplayKeepsItsIDAndDoesNotOccupyAnotherSlot(t *testing.T) {
	// Given a queued execution whose dispatch key is durable across worker restart.
	r := startTaskRuntime(t, nil, nil, nil)
	first, _ := r.spawn(t, "First")
	if err := r.worker.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When the same source repeats its dispatch, control returns the saved ID.
	replayed, _ := r.spawn(t, "First")
	if replayed.ID != first.ID {
		t.Fatalf("durable replay changed ID: first=%s replay=%s", first.ID, replayed.ID)
	}
	// Then one more execution fits, and a third distinct dispatch is rejected.
	r.spawn(t, "Second")
	if _, err := r.spawnRequest(t, "Third", map[string]string{"title": "Third", "goal": "must not run", "provider": "faux", "modelId": "echo"}); !errors.Is(err, chat.ErrTaskConflict) {
		t.Fatalf("third dispatch: %v", err)
	}
	tasks, err := r.service.ListTasks(t.Context(), "bot", r.parent)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("replay created work: tasks=%+v err=%v", tasks, err)
	}
	t.Logf("replayed=%s occupied_slots=%d", first.ID, len(tasks))
}

func TestUserBackgroundExecutionCannotManageDelegations(t *testing.T) {
	// Given a real background conversation that completed an execution.
	r := startTaskRuntime(t, nil, nil, nil)
	task, _ := r.spawn(t, "Background")
	r.executeQueued(t, task.ID)
	row, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := r.q.GetConversation(t.Context(), row.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	// When the background conversation calls any management operation directly.
	// Then control rejects every operation, even with a real owned execution ID.
	for _, action := range []string{"spawn", "list", "get", "cancel"} {
		args, err := json.Marshal(map[string]string{"id": task.ID, "title": "Forbidden", "goal": "must not run"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.service.HandleTask(t.Context(), worker.TaskRequest{AgentID: "bot", ConversationID: hidden.WorkerConversationID.Int64, SourceRequestID: task.ID, Key: "bot/forbidden-" + action, Action: action, Args: args})
		if !errors.Is(err, chat.ErrTaskInvalid) {
			t.Fatalf("background %s: %v", action, err)
		}
	}
}

func TestUserStoppedSourceCannotDispatchMoreWork(t *testing.T) {
	// Given a pending source that can create a background execution.
	r := startTaskRuntime(t, nil, nil, nil)
	// When Stop withdraws that source before its tool RPC arrives.
	if _, err := r.service.Stop(t.Context(), "bot", r.parent); err != nil {
		t.Fatal(err)
	}
	// Then the stale source cannot create another execution.
	if _, err := r.spawnRequest(t, "late-spawn", map[string]string{"title": "Late", "goal": "must not run", "provider": "faux", "modelId": "echo"}); !errors.Is(err, chat.ErrTaskConflict) {
		t.Fatalf("stopped source spawned: %v", err)
	}
}

func TestUserDelegationRejectsForeignAndStaleSources(t *testing.T) {
	// Given a main source and one hidden execution under that source.
	r := startTaskRuntime(t, nil, nil, nil)
	task, _ := r.spawn(t, "Owned")
	args, err := json.Marshal(map[string]string{"title": "Forbidden", "goal": "must not run", "provider": "faux", "modelId": "echo"})
	if err != nil {
		t.Fatal(err)
	}
	// When a request uses an unknown source or a hidden conversation's submission ID.
	// Then neither request can authorize another dispatch in the main conversation.
	for _, source := range []string{"missing-source", task.ID} {
		_, err := r.service.HandleTask(t.Context(), worker.TaskRequest{AgentID: "bot", ConversationID: r.workerParent, SourceRequestID: source, Key: "bot/forged-" + source, Action: "spawn", Args: args})
		if !errors.Is(err, chat.ErrTaskInvalid) {
			t.Fatalf("source %s: %v", source, err)
		}
	}
	tasks, err := r.service.ListTasks(t.Context(), "bot", r.parent)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("invalid sources created work: tasks=%+v err=%v", tasks, err)
	}
}

func TestUserCancellingCompletedTaskLeavesItsResultAvailable(t *testing.T) {
	// Given a completed execution with a saved report and a pending parent result.
	r := startTaskRuntime(t, nil, nil, nil)
	task, _ := r.spawn(t, "Completed")
	r.executeQueued(t, task.ID)
	before, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	resultID := "task:" + task.ID + ":result"
	resultBefore, err := r.q.GetSubmission(t.Context(), resultID)
	if err != nil || resultBefore.Status != "pending" {
		t.Fatalf("pending result: %+v err=%v", resultBefore, err)
	}
	// When the main agent cancels an execution that is already terminal.
	if _, err := r.service.CancelTask(t.Context(), "bot", r.parent, task.ID); err != nil {
		t.Fatal(err)
	}
	// Then cancellation leaves the report, stop intent, and result unchanged.
	after, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil || after.Status != before.Status || after.Report != before.Report || after.StopRequestedAt != before.StopRequestedAt {
		t.Fatalf("terminal task changed: before=%+v after=%+v err=%v", before, after, err)
	}
	resultAfter, err := r.q.GetSubmission(t.Context(), resultID)
	if err != nil || resultAfter != resultBefore {
		t.Fatalf("terminal cancellation changed result: before=%+v after=%+v err=%v", resultBefore, resultAfter, err)
	}
	t.Logf("terminal=%s status=%s result=%s", task.ID, after.Status, resultAfter.Status)
}

func TestUserFailureReportSurvivesLaterCleanupConfirmation(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "cleanup confirms"
		if restart {
			name = "worker restarts before confirmation"
		}
		t.Run(name, func(t *testing.T) {
			// Given a real failed submission whose cleanup receipt arrives separately.
			settled := make(chan worker.Settled, 1)
			r := startTaskRuntime(t, nil, nil, nil, func(_ context.Context, event worker.Settled) { settled <- event })
			task, id := r.spawn(t, "Failed report")
			row, err := r.q.GetTask(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			var created worker.CreateConversationResult
			if err := r.worker.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "bot", Model: worker.ModelRef{Provider: "faux", ModelID: "spawn"}, ThinkingLevel: "off"}, &created); err != nil {
				t.Fatal(err)
			}
			if err := r.q.SetWorkerConversation(t.Context(), sqlc.SetWorkerConversationParams{ID: row.ConversationID, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true}}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.q.StartTask(t.Context(), sqlc.StartTaskParams{ID: id, Now: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			if err := r.worker.Call(t.Context(), "submit", worker.SubmitParams{AgentID: "bot", ConversationID: created.ConversationID, TaskStageID: id, Model: worker.ModelRef{Provider: "faux", ModelID: "spawn"}, ThinkingLevel: "off", RequestID: id, Content: "invalid JSON", Attachments: []worker.Attachment{}}, &worker.SubmitResult{}); err != nil {
				t.Fatal(err)
			}
			var event worker.Settled
			select {
			case event = <-settled:
			case <-time.After(10 * time.Second):
				t.Fatal("worker did not report the model failure")
			}
			if event.Status != "unanswered" || event.Reason != "model_error" {
				t.Fatalf("natural failure: %+v", event)
			}
			// When control receives the failure before it receives exit confirmation.
			event.ExecutionConfirmed = false
			event.StopNote = "Controlled execution has not ended."
			r.service.HandleSettled(t.Context(), event)
			saved, err := r.service.GetTask(t.Context(), "bot", r.parent, id)
			if err != nil || saved.Report != event.Reason {
				t.Fatalf("failure was not saved before cleanup: %+v err=%v", saved, err)
			}
			if restart {
				if err := r.worker.Restart(t.Context()); err != nil {
					t.Fatal(err)
				}
				got, err := r.service.GetTask(t.Context(), "bot", r.parent, id)
				if err != nil || got.Status != "interrupted" || got.Report != event.Reason {
					t.Fatalf("restart lost failure evidence: %+v err=%v", got, err)
				}
				if _, err := r.q.GetSubmission(t.Context(), "task:"+id+":result"); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("restart delivered interrupted execution: %v", err)
				}
				t.Logf("execution=%s status=%s report=%q", id, got.Status, got.Report)
				return
			}
			var stopped worker.AbortResult
			if err := r.worker.Call(t.Context(), "abort", worker.AbortParams{AgentID: "bot", ConversationID: created.ConversationID, TaskStageID: id}, &stopped); err != nil {
				t.Fatal(err)
			}
			if !stopped.ExecutionConfirmed {
				t.Fatalf("cleanup was not confirmed: %+v", stopped)
			}
			r.service.HandleSettled(t.Context(), worker.Settled{RequestID: id, Status: "unanswered", Reason: "aborted", ExecutionConfirmed: stopped.ExecutionConfirmed, StopNote: stopped.StopNote})
			// Then the saved report and delivered result keep the real failure and clear the old note.
			got, err := r.service.GetTask(t.Context(), "bot", r.parent, id)
			if err != nil || got.Status != "failed" || got.Report != event.Reason || got.StopNote != stopped.StopNote {
				t.Fatalf("cleanup changed failure: %+v err=%v", got, err)
			}
			result, err := r.q.GetSubmission(t.Context(), "task:"+task.ID+":result")
			if err != nil || result.Status != "pending" || !strings.Contains(result.Content, `"report":"model_error"`) || strings.Contains(result.Content, "has not ended") {
				t.Fatalf("failure result: %+v err=%v", result, err)
			}
			if _, err := r.service.Stop(t.Context(), "bot", r.parent); err != nil {
				t.Fatal(err)
			}
			if after, err := r.service.GetTask(t.Context(), "bot", r.parent, id); err != nil || after.StopRequestedAt != nil || after.Report != event.Reason {
				t.Fatalf("Stop changed completed failure: %+v err=%v", after, err)
			}
			t.Logf("execution=%s report=%q stopNote=%q", id, got.Report, got.StopNote)
		})
	}
}
