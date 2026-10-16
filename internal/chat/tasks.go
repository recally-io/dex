package chat

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskInvalid  = errors.New("invalid task request")
	ErrTaskConflict = errors.New("task state conflicts with this request")
)

// Task is a user goal. A finished stage does not close the goal.
type Task struct {
	ID              string      `json:"id"`
	ShortID         string      `json:"shortId"`
	ChatID          int64       `json:"chatId"`
	Title           string      `json:"title"`
	Status          string      `json:"status"`
	CreatedAt       int64       `json:"createdAt"`
	LastEventAt     int64       `json:"lastEventAt"`
	EndedAt         *int64      `json:"endedAt,omitempty"`
	StopRequestedAt *int64      `json:"stopRequestedAt,omitempty"`
	Report          string      `json:"report"`
	Stages          []TaskStage `json:"stages"`
}

// TaskStage is one execution with settings fixed at creation.
type TaskStage struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	AgentID       string `json:"agentId"`
	Status        string `json:"status"`
	Provider      string `json:"provider"`
	ModelID       string `json:"modelId"`
	ThinkingLevel string `json:"thinkingLevel"`
	CreatedAt     int64  `json:"createdAt"`
	StartedAt     *int64 `json:"startedAt,omitempty"`
	EndedAt       *int64 `json:"endedAt,omitempty"`
	LastEventAt   int64  `json:"lastEventAt"`
	CurrentTool   string `json:"currentTool"`
	Report        string `json:"report"`
}

// ConfigureTasks connects the settings boundary before Run.
func (s *Service) ConfigureTasks(check func(context.Context, Settings) error) {
	s.checkTaskSettings = check
}

func taskTime(n sql.NullInt64) *int64 {
	if n.Valid {
		return new(n.Int64)
	}
	return nil
}

func taskView(ctx context.Context, q *sqlc.Queries, row sqlc.UserTask) (Task, error) {
	t := Task{ID: row.ID, ShortID: row.ShortID, ChatID: row.ParentConversationID, Title: row.Title,
		Status: row.Status, Report: row.Report, CreatedAt: row.CreatedAt, LastEventAt: row.CreatedAt,
		EndedAt: taskTime(row.EndedAt), StopRequestedAt: taskTime(row.StopRequestedAt), Stages: []TaskStage{}}
	stages, err := q.ListTaskStages(ctx, row.ID)
	if err != nil {
		return Task{}, err
	}
	for _, p := range stages {
		t.Stages = append(t.Stages, TaskStage{ID: p.ID, Title: p.Title, AgentID: p.AgentID, Status: p.Status,
			Provider: p.ModelProvider, ModelID: p.ModelID, ThinkingLevel: p.ThinkingLevel,
			CreatedAt: p.CreatedAt, StartedAt: taskTime(p.StartedAt), EndedAt: taskTime(p.EndedAt),
			LastEventAt: p.LastEventAt, CurrentTool: p.CurrentTool, Report: p.Report})
		t.LastEventAt = max(t.LastEventAt, p.LastEventAt)
	}
	if row.EndedAt.Valid {
		t.LastEventAt = max(t.LastEventAt, row.EndedAt.Int64)
	}
	if row.StopRequestedAt.Valid {
		t.LastEventAt = max(t.LastEventAt, row.StopRequestedAt.Int64)
	}
	return t, nil
}

func (s *Service) ListTasks(ctx context.Context, agentID string, parentID int64) ([]Task, error) {
	if _, err := s.Get(ctx, agentID, parentID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListUserTasks(ctx, parentID)
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(rows))
	for _, row := range rows {
		t, err := taskView(ctx, s.q, row)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (s *Service) GetTask(ctx context.Context, agentID string, parentID int64, id string) (Task, error) {
	row, err := s.q.GetUserTask(ctx, sqlc.GetUserTaskParams{AgentID: agentID, ParentID: parentID, TaskID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return taskView(ctx, s.q, row)
}

// taskTransaction obtains SQLite's writer before any reads. Other control writers cannot
// invalidate the read snapshot between the capacity check and insertion.
func (s *Service) taskTransaction(ctx context.Context) (*sql.Tx, *sqlc.Queries, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE user_tasks SET id = id WHERE 0"); err != nil {
		return nil, nil, errors.Join(err, tx.Rollback())
	}
	return tx, s.q.WithTx(tx), nil
}

// HandleTask validates the worker-owned source before accepting a model's tool arguments.
func (s *Service) HandleTask(ctx context.Context, r worker.TaskRequest) (any, error) {
	conv, err := s.q.GetConversationByWorker(ctx, sqlc.GetConversationByWorkerParams{
		AgentID: r.AgentID, WorkerConversationID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("task caller: %w", err)
	}
	id, attempt := taskRequestID(r.SourceRequestID)
	source, err := s.q.GetSubmission(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("task source: %w", err)
	}
	if source.ConversationID != conv.ID || attempt != source.DeliveryAttempt {
		return nil, fmt.Errorf("%w: source does not belong to this run", ErrTaskInvalid)
	}
	parentID := conv.ID
	if strings.HasPrefix(conv.ChannelKey, "task:") {
		if r.Action == "spawn" || r.Action == "finish" {
			return nil, fmt.Errorf("%w: task stages cannot spawn or finish goals", ErrTaskInvalid)
		}
		stage, err := s.q.GetTaskStageByConversation(ctx, conv.ID)
		if err != nil {
			return nil, err
		}
		goal, err := s.q.GetUserTaskByID(ctx, stage.UserTaskID)
		if err != nil {
			return nil, err
		}
		parentID = goal.ParentConversationID
	}
	var args struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Report string `json:"report"`
	}
	switch r.Action {
	case "spawn":
		return s.spawnTask(ctx, conv, source, r)
	case "list":
		list, err := s.ListTasks(ctx, r.AgentID, parentID)
		return struct {
			Tasks []Task `json:"tasks"`
		}{list}, err
	case "get", "cancel", "finish":
		if err := json.Unmarshal(r.Args, &args); err != nil {
			return nil, errors.Join(ErrTaskInvalid, err)
		}
		if args.ID == "" {
			return nil, fmt.Errorf("%w: task ID is required", ErrTaskInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: unknown action %q", ErrTaskInvalid, r.Action)
	}
	switch r.Action {
	case "get":
		return s.GetTask(ctx, r.AgentID, parentID, args.ID)
	case "cancel":
		return s.cancelTask(ctx, r.AgentID, parentID, args.ID, &r)
	default:
		return s.finishTask(ctx, r.AgentID, parentID, args.ID, args.Status, args.Report, r)
	}
}

// taskSource rechecks admission inside the write transaction after worker validation returns.
func taskSource(ctx context.Context, q *sqlc.Queries, r worker.TaskRequest) (sqlc.Submission, error) {
	conv, err := q.GetConversationByWorker(ctx, sqlc.GetConversationByWorkerParams{AgentID: r.AgentID, WorkerConversationID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return sqlc.Submission{}, err
	}
	id, attempt := taskRequestID(r.SourceRequestID)
	source, err := q.GetSubmission(ctx, id)
	if err != nil {
		return source, err
	}
	if source.ConversationID != conv.ID || source.DeliveryAttempt != attempt || source.Status != "pending" {
		return source, fmt.Errorf("%w: the source run is no longer active", ErrTaskConflict)
	}
	return source, nil
}

func taskRequestID(id string) (string, int64) {
	base, suffix, ok := strings.Cut(id, ":attempt:")
	if !ok {
		return id, 0
	}
	n, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil {
		return id, 0
	}
	return base, n
}

func wireTaskID(id string, attempt int64) string {
	if attempt == 0 {
		return id
	}
	return fmt.Sprintf("%s:attempt:%d", id, attempt)
}

func (s *Service) spawnTask(ctx context.Context, parent sqlc.Conversation, source sqlc.Submission, r worker.TaskRequest) (any, error) {
	var a struct {
		Title         string `json:"title"`
		Goal          string `json:"goal"`
		UserTaskID    string `json:"userTaskId"`
		Provider      string `json:"provider"`
		ModelID       string `json:"modelId"`
		ThinkingLevel string `json:"thinkingLevel"`
	}
	if err := json.Unmarshal(r.Args, &a); err != nil {
		return nil, errors.Join(ErrTaskInvalid, err)
	}
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Goal) == "" || len(a.Title) > 240 || len(a.Goal) > 128000 || !strings.HasPrefix(r.Key, r.AgentID+"/") {
		return nil, fmt.Errorf("%w: title (1–240 bytes), goal (1–128000 bytes), and a durable key are required", ErrTaskInvalid)
	}
	// Provider validation can call the worker. Do it before a lock needed by settled events.
	if prior, err := s.q.GetTaskStageByKey(ctx, r.Key); err == nil {
		goal, err := s.q.GetUserTaskByID(ctx, prior.UserTaskID)
		if err != nil {
			return nil, err
		}
		if goal.ParentConversationID != parent.ID {
			return nil, ErrTaskNotFound
		}
		t, err := taskView(ctx, s.q, goal)
		return spawnReply(t, prior.ID), err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	agent, err := s.q.GetAgent(ctx, r.AgentID)
	if err != nil {
		return nil, err
	}
	settings := Settings{Provider: cmp.Or(parent.ModelProvider, agent.ModelProvider), ModelID: cmp.Or(parent.ModelID, agent.ModelID), ThinkingLevel: cmp.Or(a.ThinkingLevel, parent.ThinkingLevel, agent.ThinkingLevel)}
	if a.Provider != "" || a.ModelID != "" {
		if a.Provider == "" || a.ModelID == "" {
			return nil, fmt.Errorf("%w: give provider and modelId together", ErrTaskInvalid)
		}
		settings.Provider, settings.ModelID = a.Provider, a.ModelID
	}
	if settings.ModelID == "" {
		return nil, ErrNoModel
	}
	if (a.Provider != "" || a.ThinkingLevel != "") && s.checkTaskSettings != nil {
		if err := s.checkTaskSettings(ctx, settings); err != nil {
			return nil, err
		}
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if prior, err := q.GetTaskStageByKey(ctx, r.Key); err == nil {
		goal, err := q.GetUserTaskByID(ctx, prior.UserTaskID)
		if err != nil {
			return nil, err
		}
		if goal.ParentConversationID != parent.ID {
			return nil, ErrTaskNotFound
		}
		t, err := taskView(ctx, q, goal)
		return spawnReply(t, prior.ID), err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	source, err = taskSource(ctx, q, r)
	if err != nil {
		return nil, err
	}
	if source.Kind == "task_result" {
		stage, err := q.GetTaskStageByResult(ctx, source.ID)
		if err != nil {
			return nil, fmt.Errorf("task result source: %w", err)
		}
		if a.UserTaskID != "" {
			goal, err := q.GetUserTask(ctx, sqlc.GetUserTaskParams{AgentID: r.AgentID, ParentID: parent.ID, TaskID: a.UserTaskID})
			if err != nil || goal.ID != stage.UserTaskID {
				return nil, fmt.Errorf("%w: a task result can only continue its original goal", ErrTaskConflict)
			}
		}
		a.UserTaskID = stage.UserTaskID
	}
	var goal sqlc.UserTask
	if a.UserTaskID != "" {
		goal, err = q.GetUserTask(ctx, sqlc.GetUserTaskParams{AgentID: r.AgentID, ParentID: parent.ID, TaskID: a.UserTaskID})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		if err != nil {
			return nil, err
		}
		if goal.StopRequestedAt.Valid || goal.EndedAt.Valid || goal.Status == "unknown" {
			return nil, fmt.Errorf("%w: this goal is stopped, closed, or has unknown execution", ErrTaskConflict)
		}
		stages, err := q.ListTaskStages(ctx, goal.ID)
		if err != nil {
			return nil, err
		}
		for _, stage := range stages {
			if stage.Status == "unknown" {
				return nil, fmt.Errorf("%w: this goal has unknown execution", ErrTaskConflict)
			}
		}
	}
	count, err := q.CountTaskSlots(ctx, r.AgentID)
	if err != nil {
		return nil, err
	}
	if count >= 2 {
		return nil, fmt.Errorf("%w: this agent already has two occupied task slots", ErrTaskConflict)
	}
	now := s.now().UnixMilli()
	if goal.ID == "" {
		var short [5]byte
		if _, err := rand.Read(short[:]); err != nil {
			return nil, err
		}
		goal = sqlc.UserTask{ID: uuid.NewV7().String(), ShortID: hex.EncodeToString(short[:]), ParentConversationID: parent.ID, Title: a.Title, Status: "active", CreatedAt: now}
		if err := q.CreateUserTask(ctx, sqlc.CreateUserTaskParams{ID: goal.ID, ShortID: goal.ShortID, ParentConversationID: parent.ID, Title: goal.Title, CreatedAt: now}); err != nil {
			return nil, err
		}
	} else {
		if err := q.SetUserTaskState(ctx, sqlc.SetUserTaskStateParams{ID: goal.ID, Status: "active"}); err != nil {
			return nil, err
		}
		goal.Status, goal.Report = "active", ""
	}
	stageID := uuid.NewV7().String()
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: r.AgentID, ChannelKey: "task:" + stageID, Title: a.Title, ModelProvider: settings.Provider, ModelID: settings.ModelID, ThinkingLevel: settings.ThinkingLevel, CreatedAt: now})
	if err != nil {
		return nil, err
	}
	if _, err = q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: stageID, ConversationID: conv.ID, Content: a.Goal, Attachments: "null", CreatedAt: now}); err != nil {
		return nil, err
	}
	if err = q.CreateTaskStage(ctx, sqlc.CreateTaskStageParams{ID: stageID, UserTaskID: goal.ID, ConversationID: conv.ID, SpawnKey: r.Key, SourceRequestID: source.ID, Title: a.Title, LastEventAt: now, CreatedAt: now}); err != nil {
		return nil, err
	}
	t, err := taskView(ctx, q, goal)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.signal(parent.ID)
	s.Wake()
	return spawnReply(t, stageID), nil
}

func spawnReply(t Task, stageID string) any {
	return struct {
		Task    Task   `json:"task"`
		StageID string `json:"stageId"`
	}{t, stageID}
}

func occupied(status string) bool {
	return status == "queued" || status == "running" || status == "cancelling" || status == "unknown"
}

func (s *Service) finishTask(ctx context.Context, agentID string, parentID int64, id, status, report string, caller worker.TaskRequest) (Task, error) {
	if (status != "done" && status != "needs_input" && status != "failed") || strings.TrimSpace(report) == "" || !strings.HasPrefix(caller.Key, agentID+"/") {
		return Task{}, fmt.Errorf("%w: final status and report are required", ErrTaskInvalid)
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetUserTask(ctx, sqlc.GetUserTaskParams{AgentID: agentID, ParentID: parentID, TaskID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if prior, err := q.GetTaskFinish(ctx, caller.Key); err == nil {
		if prior != row.ID {
			return Task{}, ErrTaskNotFound
		}
		return taskView(ctx, q, row)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	if row.StopRequestedAt.Valid {
		return Task{}, fmt.Errorf("%w: cancellation prevents finishing this goal", ErrTaskConflict)
	}
	if row.EndedAt.Valid {
		if row.Status == status && row.Report == report {
			return taskView(ctx, q, row)
		}
		return Task{}, fmt.Errorf("%w: this goal is already closed", ErrTaskConflict)
	}
	if _, err := taskSource(ctx, q, caller); err != nil {
		return Task{}, err
	}
	stages, err := q.ListTaskStages(ctx, row.ID)
	if err != nil {
		return Task{}, err
	}
	for _, p := range stages {
		if occupied(p.Status) {
			return Task{}, fmt.Errorf("%w: execution must end before finishing", ErrTaskConflict)
		}
	}
	ended := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: status != "needs_input"}
	if err = q.SetUserTaskState(ctx, sqlc.SetUserTaskStateParams{ID: row.ID, Status: status, Report: report, EndedAt: ended}); err != nil {
		return Task{}, err
	}
	row.Status, row.Report, row.EndedAt = status, report, ended
	if err = q.CreateTaskFinish(ctx, sqlc.CreateTaskFinishParams{CallKey: caller.Key, UserTaskID: row.ID}); err != nil {
		return Task{}, err
	}
	if err = s.taskNotice(ctx, q, row, "finish:"+caller.Key, report); err != nil {
		return Task{}, err
	}
	t, err := taskView(ctx, q, row)
	if err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	s.taskChanged(ctx, row)
	return t, nil
}

// CancelTask commits intent before returning. Run later confirms controlled execution ended.
func (s *Service) CancelTask(ctx context.Context, agentID string, parentID int64, id string) (Task, error) {
	return s.cancelTask(ctx, agentID, parentID, id, nil)
}

func (s *Service) cancelTask(ctx context.Context, agentID string, parentID int64, id string, caller *worker.TaskRequest) (Task, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetUserTask(ctx, sqlc.GetUserTaskParams{AgentID: agentID, ParentID: parentID, TaskID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if row.EndedAt.Valid || row.StopRequestedAt.Valid {
		return taskView(ctx, q, row)
	}
	if caller != nil {
		if _, err := taskSource(ctx, q, *caller); err != nil {
			return Task{}, err
		}
	}
	now := s.now().UnixMilli()
	if err = q.RequestTaskStop(ctx, sqlc.RequestTaskStopParams{ID: row.ID, StopRequestedAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
		return Task{}, err
	}
	if err = q.CancelTaskStages(ctx, row.ID); err != nil {
		return Task{}, err
	}
	row.StopRequestedAt = sql.NullInt64{Int64: now, Valid: true}
	row.Status = "cancelling"
	t, err := taskView(ctx, q, row)
	if err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	s.signal(parentID)
	s.Wake()
	return t, nil
}

func (s *Service) taskNotice(ctx context.Context, q *sqlc.Queries, row sqlc.UserTask, key, text string) error {
	parent, err := q.GetConversation(ctx, row.ParentConversationID)
	if err != nil {
		return err
	}
	if strings.HasPrefix(parent.ChannelKey, "web:") || strings.HasPrefix(parent.ChannelKey, "automation:") {
		return nil
	}
	return q.CreateTaskNotice(ctx, sqlc.CreateTaskNoticeParams{ID: "task:" + row.ID + ":" + key, UserTaskID: row.ID, ChannelKey: parent.ChannelKey,
		Text: fmt.Sprintf("Task %s · %s\nStatus: %s\n%s", row.ShortID, row.Title, row.Status, text), CreatedAt: s.now().UnixMilli()})
}

func (s *Service) taskChanged(ctx context.Context, row sqlc.UserTask) {
	s.signal(row.ParentConversationID)
	if s.notify != nil {
		parent, err := s.q.GetConversation(ctx, row.ParentConversationID)
		if err == nil {
			s.notify(parent.ChannelKey)
		}
	}
	s.Wake()
}

func (s *Service) HandleTaskProgress(ctx context.Context, p worker.TaskProgress) {
	if err := s.q.UpdateTaskProgress(ctx, sqlc.UpdateTaskProgressParams{ID: p.StageID, CurrentTool: p.CurrentTool, LastEventAt: p.LastEventAt}); err != nil {
		slog.Error("save task progress", "stage", p.StageID, "err", err)
	}
}

// RecoverTasks runs before any Harness opens. No old task run is resumed, even if its
// final event was lost. Records with uncertain external effects keep their slot.
func (s *Service) RecoverTasks(ctx context.Context, call worker.CallFunc) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	live, err := q.ListLiveTaskStages(ctx)
	if err != nil {
		return err
	}
	for _, p := range live {
		if p.Status == "unknown" || (p.Status == "cancelling" && !p.StartedAt.Valid) {
			continue
		}
		report := "The worker restarted. Execution is unknown. Check external processes and artifacts before starting new work."
		if err = q.MarkTaskStageUnknown(ctx, sqlc.MarkTaskStageUnknownParams{ID: p.ID, Report: report, LastEventAt: s.now().UnixMilli()}); err != nil {
			return err
		}
		goal, err := q.GetUserTaskByID(ctx, p.UserTaskID)
		if err != nil {
			return err
		}
		goal.Status, goal.Report = "unknown", report
		if err = q.SetUserTaskState(ctx, sqlc.SetUserTaskStateParams{ID: goal.ID, Status: goal.Status, Report: report}); err != nil {
			return err
		}
		if err = s.taskNotice(ctx, q, goal, p.ID+":unknown", report); err != nil {
			return err
		}
	}
	stages, err := q.ListTaskRecoveries(ctx)
	if err != nil {
		return err
	}
	params := worker.RecoverTasksParams{Stages: []worker.RecoverTaskStage{}}
	for _, p := range stages {
		params.Stages = append(params.Stages, worker.RecoverTaskStage{StageID: p.ID, AgentID: p.AgentID, ConversationID: p.WorkerConversationID.Int64})
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return call(ctx, "recoverTasks", params, &struct{}{})
}
