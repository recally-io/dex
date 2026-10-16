package chat_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
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

func startTaskRuntime(t *testing.T, beforeInit func(context.Context), onTask func(context.Context, worker.TaskRequest, *chat.Service) (any, error), onBind func(error)) *taskRuntime {
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
		Env:       []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "DEX_DATA_DIR=" + dir, "DEX_FAKE_MODEL=1"},
		OnSettled: func(ctx context.Context, event worker.Settled) { service.HandleSettled(ctx, event) },
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

func (r *taskRuntime) task(t *testing.T, key, action string, args any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return r.service.HandleTask(t.Context(), worker.TaskRequest{AgentID: "bot", ConversationID: r.workerParent, SourceRequestID: "source", Key: "bot/" + key, Action: action, Args: raw})
}

func (r *taskRuntime) spawn(t *testing.T, key, goalID string) (chat.Task, string) {
	t.Helper()
	result, err := r.task(t, key, "spawn", map[string]string{"title": key, "goal": "bounded work", "userTaskId": goalID, "provider": "faux", "modelId": "echo"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Task    chat.Task `json:"task"`
		StageID string    `json:"stageId"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Task, reply.StageID
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
	// Given a queued stage with an empty, saved worker conversation.
	r := startTaskRuntime(t, nil, nil, nil)
	goal, stageID := r.spawn(t, "Queued", "")
	if _, err := r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	stage, err := r.q.GetTaskStage(t.Context(), stageID)
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
	// When the worker restarts before the stage starts, control can deliver its first input.
	if err := r.worker.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.service.Run(ctx) }()
	t.Cleanup(func() { stop(); <-done })
	// Then the successful report releases its slot instead of becoming unknown.
	taskEventually(t, func() (bool, any) {
		got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Stages[0].Status == "unknown" {
			t.Fatalf("never-started task became unknown: %+v", got)
		}
		return got.Stages[0].Status == "done" && got.Stages[0].Report == "echo: bounded work", got
	})
}

func TestUserCancelsQueuedTaskAcrossWorkerRestart(t *testing.T) {
	for _, savedConversation := range []bool{false, true} {
		name := "without worker conversation"
		if savedConversation {
			name = "with empty worker conversation"
		}
		t.Run(name, func(t *testing.T) {
			// Given a shell stage that has not been admitted to the worker.
			r := startTaskRuntime(t, nil, nil, nil)
			marker := filepath.Join(r.dir, "must-not-run")
			if _, err := r.task(t, "cancel-before-start", "spawn", map[string]string{
				"title": "Cancel before start", "goal": "printf 'unexpected execution\\n' > '" + marker + "'", "provider": "faux", "modelId": "bash",
			}); err != nil {
				t.Fatal(err)
			}
			tasks, err := r.service.ListTasks(t.Context(), "bot", r.parent)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("queued task: %+v, %v", tasks, err)
			}
			goal, stageID := tasks[0], tasks[0].Stages[0].ID
			stage, err := r.q.GetTaskStage(t.Context(), stageID)
			if err != nil || stage.StartedAt.Valid {
				t.Fatalf("stage already started: %+v, %v", stage, err)
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
				if got.Status == "unknown" || got.Stages[0].Status == "unknown" {
					t.Fatalf("never-started cancellation became unknown: %+v", got)
				}
				return got.Status == "cancelled" && got.Stages[0].Status == "cancelled", got
			})
			stopService()
			got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
			if err != nil || got.Stages[0].StartedAt != nil {
				t.Fatalf("cancelled stage started: %+v, %v", got, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled stage left a tool artifact: %v", err)
			}
			if _, err := r.service.Post(t.Context(), "bot", "web:lifecycle", "Lifecycle", "next-source", "new work"); err != nil {
				t.Fatal(err)
			}
			spawn := func(key, goalID string) error {
				t.Helper()
				args, err := json.Marshal(map[string]string{"title": key, "goal": "bounded work", "userTaskId": goalID, "provider": "faux", "modelId": "echo"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = r.service.HandleTask(t.Context(), worker.TaskRequest{AgentID: "bot", ConversationID: r.workerParent, SourceRequestID: "next-source", Key: "bot/" + key, Action: "spawn", Args: args})
				return err
			}
			if err := spawn("continue-cancelled", goal.ID); !errors.Is(err, chat.ErrTaskConflict) {
				t.Fatalf("cancelled goal continued: %v", err)
			}
			for _, key := range []string{"next-first", "next-second"} {
				if err := spawn(key, ""); err != nil {
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
	// Given a queued stage whose empty worker conversation is already saved.
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
	goal, stageID := r.spawn(t, "Recovery window", "")
	if _, err := r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	stage, err := r.q.GetTaskStage(t.Context(), stageID)
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
		t.Fatal("stage dispatch waited for the next worker instead of retaining queued state")
	}
	got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
	if err != nil || got.Stages[0].Status != "queued" {
		t.Fatalf("recovery queued state: %+v, %v", got, err)
	}
	releaseOnce()
	if err := <-restarted; err != nil {
		t.Fatal(err)
	}
	// Then the same queued stage completes once on the ready worker.
	taskEventually(t, func() (bool, any) {
		got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Stages[0].Status == "unknown" {
			t.Fatalf("recovery admitted uncertain stage: %+v", got)
		}
		return got.Stages[0].Status == "done", got
	})
}

func TestUserReplayedTaskFinishKeepsTheLaterQuestion(t *testing.T) {
	// Given two completed stages that each lead to a different question.
	r := startTaskRuntime(t, nil, nil, nil)
	goal, first := r.spawn(t, "First", "")
	complete := func(id string) {
		t.Helper()
		if _, err := r.q.StartTaskStage(t.Context(), sqlc.StartTaskStageParams{ID: id, Now: time.Now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		r.service.HandleSettled(t.Context(), worker.Settled{RequestID: id, Status: "done", Answer: "Reviewed report", ExecutionConfirmed: true})
	}
	finish := func(key, question string) {
		t.Helper()
		if _, err := r.task(t, key, "finish", map[string]string{"id": goal.ID, "status": "needs_input", "report": question}); err != nil {
			t.Fatal(err)
		}
	}
	complete(first)
	finish("question-one", "Question one")
	_, second := r.spawn(t, "Second", goal.ID)
	complete(second)
	finish("question-two", "Question two")
	// When the first safe tool replays after a later question was saved.
	finish("question-one", "Question one")
	// Then the replay acknowledges its receipt without replacing the newer question.
	got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
	if err != nil || got.Status != "needs_input" || got.Report != "Question two" {
		t.Fatalf("replayed finish changed the current question: %+v, %v", got, err)
	}
}

func TestUserCannotContinueGoalWithAnotherUnknownStage(t *testing.T) {
	// Given two stages under one goal, with uncertain execution in the first.
	r := startTaskRuntime(t, nil, nil, nil)
	goal, first := r.spawn(t, "First", "")
	_, second := r.spawn(t, "Second", goal.ID)
	for _, id := range []string{first, second} {
		if _, err := r.q.StartTaskStage(t.Context(), sqlc.StartTaskStageParams{ID: id, Now: time.Now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	r.service.HandleSettled(t.Context(), worker.Settled{RequestID: first, Status: "unanswered", Reason: "execution connection lost"})
	// When a different stage asks for input, the unknown execution remains the goal's boundary.
	r.service.HandleSettled(t.Context(), worker.Settled{RequestID: second, Status: "done", Answer: "NEEDS_INPUT: choose a target", ExecutionConfirmed: true})
	got, err := r.service.GetTask(t.Context(), "bot", r.parent, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unknown" {
		t.Fatalf("unknown goal changed to %s after a different stage needed input", got.Status)
	}
	// Then a new stage cannot bypass the unknown execution.
	_, err = r.task(t, "Third", "spawn", map[string]string{"title": "Third", "goal": "must not run", "userTaskId": goal.ID})
	if !errors.Is(err, chat.ErrTaskConflict) {
		t.Fatalf("continued unknown goal: %v", err)
	}
}
