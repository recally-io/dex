package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
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

// Task is one internal execution. Its ID is used only by the dispatching agent.
type Task struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Provider      string `json:"provider"`
	ModelID       string `json:"modelId"`
	ThinkingLevel string `json:"thinkingLevel"`
	CreatedAt     int64  `json:"createdAt"`
	StartedAt     *int64 `json:"startedAt,omitempty"`
	EndedAt       *int64 `json:"endedAt,omitempty"`
	Report        string `json:"report"`
}

func (s *Service) ConfigureTasks(check func(context.Context, Settings) error) {
	s.checkTaskSettings = check
}

func taskTime(n sql.NullInt64) *int64 {
	if n.Valid {
		return new(n.Int64)
	}
	return nil
}

func taskView(ctx context.Context, q *sqlc.Queries, row sqlc.Task) (Task, error) {
	c, err := q.GetConversation(ctx, row.ConversationID)
	if err != nil {
		return Task{}, err
	}
	return Task{ID: row.ID, Title: row.Title, Status: row.Status,
		Provider: c.ModelProvider, ModelID: c.ModelID, ThinkingLevel: c.ThinkingLevel,
		CreatedAt: row.CreatedAt, StartedAt: taskTime(row.StartedAt), EndedAt: taskTime(row.EndedAt),
		Report: row.Report}, nil
}

func (s *Service) ListTasks(ctx context.Context, agentID string, parentID int64) ([]Task, error) {
	if _, err := s.Get(ctx, agentID, parentID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListTasks(ctx, parentID)
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

func ownedTask(ctx context.Context, q *sqlc.Queries, agentID string, parentID int64, id string) (sqlc.Task, error) {
	row, err := q.GetTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ParentConversationID != parentID) {
		return row, ErrTaskNotFound
	}
	if err != nil {
		return row, err
	}
	parent, err := q.GetConversation(ctx, parentID)
	if err != nil {
		return row, err
	}
	if parent.AgentID != agentID {
		return row, ErrTaskNotFound
	}
	return row, nil
}

func (s *Service) GetTask(ctx context.Context, agentID string, parentID int64, id string) (Task, error) {
	row, err := ownedTask(ctx, s.q, agentID, parentID, id)
	if err != nil {
		return Task{}, err
	}
	return taskView(ctx, s.q, row)
}

// Acquire SQLite's writer before checking the source and capacity.
func (s *Service) taskTransaction(ctx context.Context) (*sql.Tx, *sqlc.Queries, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET id = id WHERE 0"); err != nil {
		return nil, nil, errors.Join(err, tx.Rollback())
	}
	return tx, s.q.WithTx(tx), nil
}

// HandleTask accepts only worker-owned sources in a main conversation.
func (s *Service) HandleTask(ctx context.Context, r worker.TaskRequest) (any, error) {
	conv, err := s.q.GetConversationByWorker(ctx, sqlc.GetConversationByWorkerParams{
		AgentID: r.AgentID, WorkerConversationID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("task caller: %w", err)
	}
	if strings.HasPrefix(conv.ChannelKey, "task:") || strings.HasPrefix(conv.ChannelKey, "dream:") {
		return nil, fmt.Errorf("%w: background conversations cannot manage tasks", ErrTaskInvalid)
	}
	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.Join(ErrTaskInvalid, fmt.Errorf("task source: %w", err))
	}
	if err != nil {
		return nil, fmt.Errorf("task source: %w", err)
	}
	if source.ConversationID != conv.ID {
		return nil, fmt.Errorf("%w: source does not belong to this run", ErrTaskInvalid)
	}
	switch r.Action {
	case "spawn":
		return s.spawnTask(ctx, conv, r)
	case "list":
		tasks, err := s.ListTasks(ctx, r.AgentID, conv.ID)
		return struct {
			Tasks []Task `json:"tasks"`
		}{tasks}, err
	case "get", "cancel":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(r.Args, &args); err != nil || args.ID == "" {
			return nil, fmt.Errorf("%w: task ID is required", ErrTaskInvalid)
		}
		if r.Action == "get" {
			return s.GetTask(ctx, r.AgentID, conv.ID, args.ID)
		}
		return s.cancelTask(ctx, r.AgentID, conv.ID, args.ID, &r)
	default:
		return nil, fmt.Errorf("%w: unknown action %q", ErrTaskInvalid, r.Action)
	}
}

func taskSource(ctx context.Context, q *sqlc.Queries, r worker.TaskRequest) (sqlc.Submission, error) {
	conv, err := q.GetConversationByWorker(ctx, sqlc.GetConversationByWorkerParams{AgentID: r.AgentID, WorkerConversationID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return sqlc.Submission{}, err
	}
	source, err := q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return source, err
	}
	if source.ConversationID != conv.ID || source.Status != "pending" {
		return source, fmt.Errorf("%w: the source run is no longer active", ErrTaskConflict)
	}
	return source, nil
}

func (s *Service) spawnTask(ctx context.Context, parent sqlc.Conversation, r worker.TaskRequest) (any, error) {
	var a struct {
		Title         string `json:"title"`
		Goal          string `json:"goal"`
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
	if prior, err := s.q.GetTaskByKey(ctx, r.Key); err == nil {
		if prior.ParentConversationID != parent.ID {
			return nil, ErrTaskNotFound
		}
		return spawnReply(prior.ID), nil
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
	if prior, err := q.GetTaskByKey(ctx, r.Key); err == nil {
		if prior.ParentConversationID != parent.ID {
			return nil, ErrTaskNotFound
		}
		return spawnReply(prior.ID), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	source, err := taskSource(ctx, q, r)
	if err != nil {
		return nil, err
	}
	// Cancellation frees admission; a tool that ignores abort can outlive this limit.
	count, err := q.CountTaskSlots(ctx, r.AgentID)
	if err != nil {
		return nil, err
	}
	if count >= 2 {
		return nil, fmt.Errorf("%w: this agent already has two active executions", ErrTaskConflict)
	}
	now, id := s.now().UnixMilli(), uuid.NewV7().String()
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: r.AgentID, ChannelKey: "task:" + id, Title: a.Title, ModelProvider: settings.Provider, ModelID: settings.ModelID, ThinkingLevel: settings.ThinkingLevel, CreatedAt: now})
	if err != nil {
		return nil, err
	}
	if _, err = q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: id, ConversationID: conv.ID, Content: a.Goal, Attachments: "null", Origin: "task", Kind: "message", CreatedAt: now}); err != nil {
		return nil, err
	}
	if err = q.CreateTask(ctx, sqlc.CreateTaskParams{ID: id, ParentConversationID: parent.ID, ConversationID: conv.ID, SpawnKey: r.Key, SourceRequestID: source.ID, Title: a.Title, CreatedAt: now}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.Wake()
	return spawnReply(id), nil
}

func spawnReply(id string) any {
	return struct {
		ID string `json:"id"`
	}{id}
}

func occupied(status string) bool {
	return status == "queued" || status == "running"
}

func (s *Service) CancelTask(ctx context.Context, agentID string, parentID int64, id string) (Task, error) {
	return s.cancelTask(ctx, agentID, parentID, id, nil)
}

func (s *Service) cancelTask(ctx context.Context, agentID string, parentID int64, id string, caller *worker.TaskRequest) (Task, error) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	t, target, call, err := s.withdrawTask(ctx, agentID, parentID, id, caller)
	if err != nil {
		return Task{}, err
	}
	if target.ConversationID != 0 {
		s.abort(ctx, call, target)
	}
	s.inFlight.Delete(id)
	s.Wake()
	return t, nil
}

func (s *Service) withdrawTask(ctx context.Context, agentID string, parentID int64, id string, caller *worker.TaskRequest) (Task, worker.AbortParams, worker.CallFunc, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return Task{}, worker.AbortParams{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := ownedTask(ctx, q, agentID, parentID, id)
	if err != nil {
		return Task{}, worker.AbortParams{}, nil, err
	}
	if caller != nil {
		if _, err := taskSource(ctx, q, *caller); err != nil {
			return Task{}, worker.AbortParams{}, nil, err
		}
	}
	var target worker.AbortParams
	var call worker.CallFunc
	if occupied(row.Status) {
		now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
		target, err = cancelTaskInput(ctx, q, row, now)
		if err != nil {
			return Task{}, target, nil, err
		}
		row.Status, row.EndedAt = "cancelled", now
		if target.ConversationID != 0 {
			call, _ = s.worker.Bind()
		}
	}
	t, err := taskView(ctx, q, row)
	if err != nil {
		return Task{}, target, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, target, nil, err
	}
	return t, target, call, nil
}

// RecoverTasks isolates admitted executions before any Harness can resume them.
func (s *Service) RecoverTasks(ctx context.Context, call worker.CallFunc) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	live, err := q.ListLiveTasks(ctx)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	for _, t := range live {
		report := cmp.Or(t.Report, "The worker restarted. Check artifacts and prior side effects before starting new work.")
		if _, err = q.SettleTask(ctx, sqlc.SettleTaskParams{ID: t.ID, Status: "interrupted", Report: report, EndedAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
			return err
		}
		if _, err = q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: t.ID, Status: "unanswered", Reason: sql.NullString{String: "interrupted", Valid: true}, SettledAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
			return err
		}
	}
	stages, err := q.ListTaskRecoveries(ctx)
	if err != nil {
		return err
	}
	params := worker.RecoverTasksParams{Stages: []worker.RecoverTaskStage{}}
	for _, t := range stages {
		params.Stages = append(params.Stages, worker.RecoverTaskStage{StageID: t.ID, AgentID: t.AgentID, ConversationID: t.WorkerConversationID.Int64})
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return call(ctx, "recoverTasks", params, &struct{}{})
}
