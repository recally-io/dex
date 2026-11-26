package server

import (
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db/sqlc"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func (h *handler) ListTasks(ctx context.Context, req api.ListTasksRequestObject) (api.ListTasksResponseObject, error) {
	p := req.Params
	rows, err := h.Tasks.ListTasks(ctx, tasksvc.Filter{
		BotID: deref(p.BotId), Status: stringValue(p.Status), Source: deref(p.Source),
		IncludeClosed: p.IncludeClosed != nil && *p.IncludeClosed, Blocked: p.Blocked, InReview: p.InReview,
	})
	if err != nil {
		return nil, taskError(err)
	}
	out := api.ListTasks200JSONResponse{Tasks: make([]api.TaskSummary, len(rows))}
	for i, row := range rows {
		out.Tasks[i] = taskSummary(row)
	}
	return out, nil
}

func (h *handler) CreateTask(ctx context.Context, req api.CreateTaskRequestObject) (api.CreateTaskResponseObject, error) {
	b := req.Body
	if b == nil {
		return nil, errInvalid
	}
	row, err := h.Tasks.CreateTask(ctx, tasksvc.Owner, tasksvc.OriginUser, tasksvc.CreateTaskParams{
		AssigneeBotID: b.AssigneeBotId, Title: b.Title, Status: stringValue(b.Status), Note: deref(b.Note),
		ReviewerKind: stringValue(b.ReviewerKind), ReviewerID: deref(b.ReviewerId),
	})
	if err != nil {
		return nil, taskError(err)
	}
	return api.CreateTask201JSONResponse(taskSummary(tasksvc.Summary{Task: row})), nil
}

func (h *handler) GetTask(ctx context.Context, req api.GetTaskRequestObject) (api.GetTaskResponseObject, error) {
	detail, err := h.Tasks.Detail(ctx, req.TaskKey)
	if err != nil {
		return nil, err
	}
	out := api.GetTask200JSONResponse{
		Task:      taskSummary(tasksvc.NewSummary(detail.Task, len(detail.Blockers), detail.Sessions)),
		Entries:   make([]api.TaskEntry, len(detail.Entries)),
		Sessions:  make([]api.TaskSession, len(detail.Sessions)),
		Questions: make([]api.TaskQuestion, len(detail.Questions)),
		Blockers:  make([]api.TaskBlocker, len(detail.Blockers)), Links: taskLinks(detail),
	}
	if detail.ParentTask != nil {
		out.ParentTask = new(taskViewRef(*detail.ParentTask))
	}
	out.CanSplit = detail.Task.AuthorizedByRootID.Valid && tasksvc.IsOpenStatus(detail.Task.Status)
	out.Subtasks = taskViewRefs(detail.Subtasks)
	out.Dependencies = taskViewRefs(detail.Dependencies)
	for i, e := range detail.Entries {
		entry, err := taskEntry(e)
		if err != nil {
			return nil, err
		}
		out.Entries[i] = entry
	}
	for i, s := range detail.Sessions {
		out.Sessions[i] = taskSession(s)
	}
	for i, b := range detail.Blockers {
		out.Blockers[i] = api.TaskBlocker{
			Kind: api.TaskBlockerKind(b.Kind), Namespace: optional(b.Namespace), ObjectType: optional(b.ObjectType),
			ObjectKey: optional(b.ObjectKey), EventType: optional(b.EventType), QuestionId: optional(b.QuestionID),
			TaskKey: optional(b.TaskKey), Question: optional(b.Question), WaitId: nonzero(b.WaitID), QueuePosition: nonzero(b.QueuePosition),
		}
	}
	for i, q := range detail.Questions {
		var options []string
		if err := json.Unmarshal([]byte(q.OptionsJson), &options); err != nil {
			return nil, err
		}
		if options == nil {
			options = []string{}
		}
		out.Questions[i] = api.TaskQuestion{
			Id: q.ID, Text: q.Text, Options: options, Queued: q.Queued != 0, AgentId: q.AgentID, SessionId: q.SessionID,
		}
	}
	return out, nil
}

func (h *handler) UpdateTask(ctx context.Context, req api.UpdateTaskRequestObject) (api.UpdateTaskResponseObject, error) {
	b, rev := req.Body, req.Params.IfMatch
	if b == nil || rev != nil && *rev < 0 {
		return nil, errInvalid
	}
	row, err := h.Tasks.UpdateTask(ctx, req.TaskKey, tasksvc.UpdateParams{
		Title: b.Title, Status: (*string)(b.Status), AssigneeBotID: b.AssigneeBotId,
		ReviewerKind: (*string)(b.ReviewerKind), ReviewerID: b.ReviewerId, Revision: rev,
	})
	if err != nil {
		return nil, taskError(err)
	}
	return api.UpdateTask200JSONResponse(taskSummary(row)), nil
}

func (h *handler) SplitTask(ctx context.Context, req api.SplitTaskRequestObject) (api.SplitTaskResponseObject, error) {
	b, rev := req.Body, req.Params.IfMatch
	if b == nil || rev != nil && *rev < 0 {
		return nil, errInvalid
	}
	var dependencies []string
	if b.DependsOn != nil {
		dependencies = *b.DependsOn
	}
	row, err := h.Tasks.SplitTask(ctx, tasksvc.Owner, tasksvc.OriginUser, req.TaskKey, tasksvc.SplitTaskParams{
		Title: b.Title, AssigneeBotID: deref(b.AssigneeBotId), Note: deref(b.Note),
		Status: stringValue(b.Status), Reviewer: stringValue(b.Reviewer), DependsOn: dependencies,
	}, rev)
	if err != nil {
		return nil, taskError(err)
	}
	return api.SplitTask201JSONResponse(taskSummary(row)), nil
}

func (h *handler) AddTaskDependency(ctx context.Context, req api.AddTaskDependencyRequestObject) (api.AddTaskDependencyResponseObject, error) {
	b, rev := req.Body, req.Params.IfMatch
	if b == nil || rev != nil && *rev < 0 {
		return nil, errInvalid
	}
	if err := h.Tasks.SetTaskDependency(ctx, req.TaskKey, b.DependsOn, true, rev); err != nil {
		return nil, taskError(err)
	}
	return api.AddTaskDependency204Response{}, nil
}

func (h *handler) RemoveTaskDependency(ctx context.Context, req api.RemoveTaskDependencyRequestObject) (api.RemoveTaskDependencyResponseObject, error) {
	rev := req.Params.IfMatch
	if rev != nil && *rev < 0 {
		return nil, errInvalid
	}
	if err := h.Tasks.SetTaskDependency(ctx, req.TaskKey, req.DepKey, false, rev); err != nil {
		return nil, taskError(err)
	}
	return api.RemoveTaskDependency204Response{}, nil
}

func taskViewRef(task sqlc.Task) api.TaskRef {
	return api.TaskRef{Key: tasksvc.FormatKey(task.Number), Title: task.Title, Status: api.TaskStatus(task.Status), AssigneeBotId: task.AssigneeBotID}
}

func taskViewRefs(tasks []sqlc.Task) []api.TaskRef {
	out := make([]api.TaskRef, len(tasks))
	for i, task := range tasks {
		out[i] = taskViewRef(task)
	}
	return out
}

func (h *handler) CreateTaskEntry(ctx context.Context, req api.CreateTaskEntryRequestObject) (api.CreateTaskEntryResponseObject, error) {
	b, rev := req.Body, req.Params.IfMatch
	if b == nil || rev != nil && *rev < 0 {
		return nil, errInvalid
	}
	entry, err := h.Tasks.PostEntry(ctx, tasksvc.Owner, tasksvc.OriginUser, req.TaskKey, tasksvc.PostEntryParams{
		Kind: stringValue(b.Kind), Tag: stringValue(b.Tag), Content: b.Content,
	}, rev)
	if err != nil {
		return nil, taskError(err)
	}
	out, err := taskEntry(entry)
	return api.CreateTaskEntry201JSONResponse(out), err
}

func (h *handler) ReviewTask(ctx context.Context, req api.ReviewTaskRequestObject) (api.ReviewTaskResponseObject, error) {
	b := req.Body
	if b == nil {
		return nil, errInvalid
	}
	var seq int64
	if b.ReviewSeq != nil {
		seq = *b.ReviewSeq
	}
	row, err := h.Tasks.Review(ctx, tasksvc.Owner, tasksvc.OriginUser, req.TaskKey, string(b.Decision), seq, deref(b.Note))
	if err != nil {
		return nil, taskError(err)
	}
	return api.ReviewTask200JSONResponse(taskSummary(row)), nil
}

func taskSummary(row tasksvc.Summary) api.TaskSummary {
	t := row.Task
	out := api.TaskSummary{
		Id: t.ID, Key: tasksvc.FormatKey(t.Number), Number: t.Number, Title: t.Title, Status: api.TaskStatus(t.Status),
		AssigneeBotId: t.AssigneeBotID, ReviewerKind: api.TaskReviewerKind(t.ReviewerKind), ReviewerId: t.ReviewerID,
		ReviewSeq: t.ReviewSeq, Revision: t.Revision, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Blocked: row.BlockerCount > 0, BlockerCount: int64(row.BlockerCount),
	}
	if row.PrimarySession != nil {
		out.PrimarySession = new(taskSession(*row.PrimarySession))
	}
	return out
}

func taskSession(s sqlc.ListTaskSessionsRow) api.TaskSession {
	out := api.TaskSession{
		SessionId: s.SessionID, AgentId: s.AgentID, AgentName: s.AgentName, Title: s.Title,
		IsPrimary: s.IsPrimary == 1, LinkedAt: s.LinkedAt, State: api.TaskSessionState(s.State),
	}
	if s.EndedAt.Valid {
		out.EndedAt = new(s.EndedAt.Int64)
	}
	return out
}

func taskEntry(e sqlc.TaskEntry) (api.TaskEntry, error) {
	out := api.TaskEntry{
		Id: e.ID, Seq: e.Seq, Kind: api.TaskEntryKind(e.Kind), Tag: e.Tag, ActorKind: e.ActorKind,
		ActorBotId: e.ActorBotID, Trust: api.TaskEntryTrust(e.Trust), Content: e.Content, CreatedAt: e.CreatedAt,
	}
	if e.EventID.Valid {
		out.EventId = new(e.EventID.String)
	}
	if e.Kind == tasksvc.KindStatus || e.Kind == tasksvc.KindReview {
		var meta struct {
			Status   *api.TaskStatus        `json:"status"`
			Decision *api.TaskEntryDecision `json:"decision"`
		}
		if err := json.Unmarshal([]byte(e.MetaJson), &meta); err != nil {
			return out, err
		}
		out.Status, out.Decision = meta.Status, meta.Decision
	}
	return out, nil
}

func nonzero(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return new(n)
}

func stringValue[S ~string](s *S) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func taskError(err error) error {
	if errors.Is(err, tasksvc.ErrInvalid) {
		return errors.Join(errInvalid, err)
	}
	return err
}

// taskLinks groups the waits of a Task under the objects they watch.
func taskLinks(detail tasksvc.Detail) []api.TaskLink {
	type key struct{ namespace, objectType, object string }
	waits := make(map[key][]api.TaskWait)
	for _, w := range detail.Waits {
		k := key{w.Namespace, w.ObjectType, w.ObjectKey}
		waits[k] = append(waits[k], api.TaskWait{Id: w.ID, Event: w.EventType, Revision: w.Revision, AfterAt: w.AfterAt, Resolved: w.ResolvedBy.Valid, Alert: w.Alert == 1})
	}
	links := make([]api.TaskLink, 0, len(detail.Objects))
	for _, o := range detail.Objects {
		link := api.TaskLink{Namespace: o.Namespace, Type: o.ObjectType, Key: o.ObjectKey, Waits: waits[key{o.Namespace, o.ObjectType, o.ObjectKey}]}
		if link.Waits == nil {
			link.Waits = []api.TaskWait{}
		}
		links = append(links, link)
	}
	return links
}
