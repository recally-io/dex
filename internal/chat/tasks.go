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
	"github.com/recally-io/dex/internal/engine"
)

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskInvalid  = errors.New("invalid task request")
	ErrTaskConflict = errors.New("task state conflicts with this request")
)

// Task is one internal execution. Its ID is used only by the dispatching agent.
type Task engine.TaskInfo

func (s *Service) ConfigureTasks(check func(context.Context, Settings) error) {
	s.checkTaskSettings = check
}

func taskTime(n sql.NullInt64) *int64 {
	if n.Valid {
		return new(n.Int64)
	}
	return nil
}

func (s *Service) taskView(ctx context.Context, q *sqlc.Queries, row sqlc.Task, waitReason string) (Task, error) {
	c, err := q.GetConversation(ctx, row.ConversationID)
	if err != nil {
		return Task{}, err
	}
	// The task's own submission keeps its immutable goal.
	goal, err := q.GetSubmission(ctx, row.ID)
	if err != nil {
		return Task{}, err
	}
	var constraints, criteria []string
	if err := json.Unmarshal([]byte(row.ConstraintsJson), &constraints); err != nil {
		return Task{}, err
	}
	if err := json.Unmarshal([]byte(row.DoneCriteriaJson), &criteria); err != nil {
		return Task{}, err
	}
	var report *engine.TaskReport
	if row.StructuredReport != "" {
		if err := json.Unmarshal([]byte(row.StructuredReport), &report); err != nil {
			return Task{}, err
		}
	}
	view := Task{ID: row.ID, Title: row.Title, Status: row.Status,
		Goal: goal.Content, Constraints: constraints, DoneCriteria: criteria, StructuredReport: report, HasStructuredReport: report != nil,
		GoalMet: row.Status == "done" && report != nil && report.Outcome == "done", WaitReason: waitReason,
		Provider: c.ModelProvider, ModelID: c.ModelID, ThinkingLevel: c.ThinkingLevel,
		CreatedAt: row.CreatedAt, StartedAt: taskTime(row.StartedAt), EndedAt: taskTime(row.EndedAt),
		Report: row.Report}
	if row.Status == "running" && c.ExecutionID.Valid {
		ref := engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64}
		a := s.engine.Activity([]engine.Ref{ref})[ref]
		view.LastAction, view.LastActivityAt, view.ToolStartedAt = a.LastAction, a.LastActivityAt, a.ToolStartedAt
	}
	return view, nil
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
	admissions, err := s.q.ListTaskAdmissions(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		conv, err := s.q.GetConversation(ctx, row.ConversationID)
		if err != nil {
			return nil, err
		}
		waitReason := taskWaitReasonFrom(admissions, row, conv)
		t, err := s.taskView(ctx, s.q, row, waitReason)
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
	waitReason, err := s.taskWaitReason(ctx, row)
	if err != nil && row.Status == "queued" {
		waitReason = "unknown"
	}
	return s.taskView(ctx, s.q, row, waitReason)
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

// taskCaller validates the execution owner before exposing task operations.
func (s *Service) taskCaller(ctx context.Context, r engine.TaskContext) (sqlc.Conversation, error) {
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: r.AgentID, ExecutionID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return conv, err
	}
	if strings.HasPrefix(conv.ChannelKey, "task:") || strings.HasPrefix(conv.ChannelKey, "dream:") {
		return conv, ErrTaskInvalid
	}
	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return conv, err
	}
	if source.ConversationID != conv.ID {
		return conv, ErrTaskInvalid
	}
	return conv, nil
}
func (s *Service) SpawnTask(ctx context.Context, r engine.TaskContext, a engine.SpawnTaskArgs) (Task, error) {
	parent, err := s.taskCaller(ctx, r)
	if err != nil {
		return Task{}, err
	}
	id, err := s.spawnTask(ctx, parent, r, a)
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, r.AgentID, parent.ID, id)
}
func (s *Service) ToolListTasks(ctx context.Context, r engine.TaskContext) ([]Task, error) {
	c, err := s.taskCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	return s.ListTasks(ctx, r.AgentID, c.ID)
}
func (s *Service) ToolGetTask(ctx context.Context, r engine.TaskContext, id string) (Task, error) {
	c, err := s.taskCaller(ctx, r)
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, r.AgentID, c.ID, id)
}
func (s *Service) ToolCancelTask(ctx context.Context, r engine.TaskContext, id string) (Task, error) {
	c, err := s.taskCaller(ctx, r)
	if err != nil {
		return Task{}, err
	}
	return s.cancelTask(ctx, r.AgentID, c.ID, id, &r)
}

func taskSource(ctx context.Context, q *sqlc.Queries, r engine.TaskContext) (sqlc.Submission, error) {
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: r.AgentID, ExecutionID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
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

func (s *Service) spawnTask(ctx context.Context, parent sqlc.Conversation, r engine.TaskContext, a engine.SpawnTaskArgs) (string, error) {
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Goal) == "" || len(a.Title) > 240 || len(a.Goal) > 128000 || !strings.HasPrefix(r.Key, r.AgentID+"/") {
		return "", fmt.Errorf("%w: title (1–240 bytes), goal (1–128000 bytes), and a durable key are required", ErrTaskInvalid)
	}
	for _, items := range [][]string{a.Constraints, a.DoneCriteria} {
		for _, item := range items {
			if strings.TrimSpace(item) == "" {
				return "", fmt.Errorf("%w: contract entries must not be empty", ErrTaskInvalid)
			}
		}
	}
	constraints, err := json.Marshal(a.Constraints)
	if err != nil {
		return "", err
	}
	criteria, err := json.Marshal(a.DoneCriteria)
	if err != nil {
		return "", err
	}
	if prior, err := s.q.GetTaskByKey(ctx, r.Key); err == nil {
		if prior.ParentConversationID != parent.ID {
			return "", ErrTaskNotFound
		}
		return prior.ID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	agent, err := s.q.GetAgent(ctx, r.AgentID)
	if err != nil {
		return "", err
	}
	settings := Settings{Provider: cmp.Or(parent.ModelProvider, agent.ModelProvider), ModelID: cmp.Or(parent.ModelID, agent.ModelID), ThinkingLevel: cmp.Or(a.ThinkingLevel, parent.ThinkingLevel, agent.ThinkingLevel)}
	if a.Provider != "" || a.ModelID != "" {
		if a.Provider == "" || a.ModelID == "" {
			return "", fmt.Errorf("%w: give provider and modelId together", ErrTaskInvalid)
		}
		settings.Provider, settings.ModelID = a.Provider, a.ModelID
	}
	if settings.ModelID == "" {
		return "", ErrNoModel
	}
	if (a.Provider != "" || a.ThinkingLevel != "") && s.checkTaskSettings != nil {
		if err := s.checkTaskSettings(ctx, settings); err != nil {
			return "", err
		}
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if prior, err := q.GetTaskByKey(ctx, r.Key); err == nil {
		if prior.ParentConversationID != parent.ID {
			return "", ErrTaskNotFound
		}
		return prior.ID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	source, err := taskSource(ctx, q, r)
	if err != nil {
		return "", err
	}
	// A bot may retain at most 20 queued or running tasks. Increase this only after
	// measuring control-store and scheduler latency under larger queues.
	const maxTaskSlots = 20
	count, err := q.CountTaskSlots(ctx, r.AgentID)
	if err != nil {
		return "", err
	}
	if count >= maxTaskSlots {
		return "", fmt.Errorf("%w: this bot already has %d queued or running tasks", ErrTaskConflict, maxTaskSlots)
	}
	now, id := s.now().UnixMilli(), uuid.NewV7().String()
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: r.AgentID, ChannelKey: "task:" + id, Title: a.Title, ModelProvider: settings.Provider, ModelID: settings.ModelID, ThinkingLevel: settings.ThinkingLevel, CreatedAt: now})
	if err != nil {
		return "", err
	}
	if err = q.SetConversationCwd(ctx, sqlc.SetConversationCwdParams{Cwd: parent.Cwd, ID: conv.ID, AgentID: r.AgentID}); err != nil {
		return "", err
	}
	if _, err = q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: id, ConversationID: conv.ID, Content: a.Goal, Attachments: "null", Origin: "task", Kind: "message", CreatedAt: now}); err != nil {
		return "", err
	}
	modifiesFiles := int64(1)
	if a.ModifiesFiles != nil && !*a.ModifiesFiles {
		modifiesFiles = 0
	}
	if err = q.CreateTask(ctx, sqlc.CreateTaskParams{ID: id, ParentConversationID: parent.ID, ConversationID: conv.ID, SpawnKey: r.Key, SourceRequestID: source.ID, Title: a.Title, ConstraintsJson: string(constraints), DoneCriteriaJson: string(criteria), ModifiesFiles: modifiesFiles, CreatedAt: now}); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	s.signal(conv.ID)
	s.signal(parent.ID)
	s.Wake()
	return id, nil
}

func occupied(status string) bool {
	return status == "queued" || status == "admitting" || status == "running"
}

const (
	waitReasonBotLimit    = "bot limit"
	waitReasonGlobalLimit = "global limit"
	waitReasonEarlier     = "waiting for an earlier task"
)

func admissionCwd(agentID, cwd string) string {
	if cwd == "" {
		return "agent:" + agentID
	}
	return "cwd:" + cwd
}

func (s *Service) taskWaitReason(ctx context.Context, task sqlc.Task) (string, error) {
	if task.Status != "queued" {
		return "", nil
	}
	admissions, err := s.q.ListTaskAdmissions(ctx)
	if err != nil {
		return "", err
	}
	conv, err := s.q.GetConversation(ctx, task.ConversationID)
	if err != nil {
		return "", err
	}
	return taskWaitReasonFrom(admissions, task, conv), nil
}

func taskWaitReasonFrom(admissions []sqlc.ListTaskAdmissionsRow, task sqlc.Task, conv sqlc.Conversation) string {
	for _, candidate := range admissions {
		if candidate.ID == task.ID {
			break
		}
		if candidate.AgentID == conv.AgentID && candidate.Status == "queued" {
			return waitReasonEarlier
		}
	}
	botRunning, globalRunning := int64(0), int64(0)
	for _, candidate := range admissions {
		if candidate.Status != "admitting" && candidate.Status != "running" {
			continue
		}
		globalRunning++
		if candidate.AgentID == conv.AgentID {
			botRunning++
		}
		if admissionCwd(candidate.AgentID, candidate.Cwd) == admissionCwd(conv.AgentID, conv.Cwd) && (task.ModifiesFiles != 0 || candidate.ModifiesFiles != 0) {
			return fmt.Sprintf("cwd held by task %s", candidate.ID)
		}
	}
	// A bot may run at most four tasks. Raise this after measuring provider fairness.
	if botRunning >= 4 {
		return waitReasonBotLimit
	}
	// The process may run at most eight tasks across all bots. Raise this after measuring host load.
	if globalRunning >= 8 {
		return waitReasonGlobalLimit
	}
	return ""
}

func (s *Service) CancelTask(ctx context.Context, agentID string, parentID int64, id string) (Task, error) {
	return s.cancelTask(ctx, agentID, parentID, id, nil)
}

func (s *Service) cancelTask(ctx context.Context, agentID string, parentID int64, id string, caller *engine.TaskContext) (Task, error) {
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return Task{}, err
	}
	defer func() { unlock(); s.Wake() }()
	t, target, err := s.withdrawTask(ctx, agentID, parentID, id, caller)
	if err != nil {
		return Task{}, err
	}
	if target.ID != 0 {
		if _, err := s.abort(ctx, target); err != nil {
			s.scheduleRetry()
			return Task{}, err
		}
	}
	s.Wake()
	return t, nil
}

func (s *Service) withdrawTask(ctx context.Context, agentID string, parentID int64, id string, caller *engine.TaskContext) (Task, engine.Ref, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return Task{}, engine.Ref{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := ownedTask(ctx, q, agentID, parentID, id)
	if err != nil {
		return Task{}, engine.Ref{}, err
	}
	if caller != nil {
		if _, err := taskSource(ctx, q, *caller); err != nil {
			return Task{}, engine.Ref{}, err
		}
	}
	var target engine.Ref

	if occupied(row.Status) {
		now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
		target, err = cancelTaskInput(ctx, q, row, now)
		if err != nil {
			return Task{}, target, err
		}
		row.Status, row.EndedAt = "cancelled", now

	}
	t, err := s.taskView(ctx, q, row, "")
	if err != nil {
		return Task{}, target, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, target, err
	}
	s.signal(row.ConversationID)
	s.signal(parentID)
	return t, target, nil
}

// RecoverTasks isolates admitted executions before any Harness can resume them.
func (s *Service) RecoverTasks(ctx context.Context) error {
	live, err := s.q.ListLiveTasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range live {
		conv, err := s.q.GetConversation(ctx, t.ConversationID)
		if err != nil {
			return err
		}
		var outcome *engine.Outcome
		if conv.ExecutionID.Valid {
			outcome, err = s.engine.RecoveryOutcome(ctx, engine.Ref{AgentID: conv.AgentID, ID: conv.ExecutionID.Int64}, t.ID)
			if err != nil {
				return err
			}
		}
		status := ""
		if outcome == nil {
			status = "interrupted"
			outcome = &engine.Outcome{Status: "unanswered", Reason: "interrupted"}
		}
		if _, err = s.settleTaskOutcome(ctx, t.ID, *outcome, status); err != nil {
			return err
		}
	}
	aborts, err := s.q.ListPendingExecutionAborts(ctx)
	if err != nil {
		return err
	}
	refs := make([]engine.Ref, 0, len(aborts))
	for _, c := range aborts {
		refs = append(refs, engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64})
	}
	return s.engine.Recover(ctx, refs)
}
