package server

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worktask"
)

func workTask(t sqlc.WorkTask) api.WorkTask {
	return api.WorkTask{Id: t.ID, AgentId: t.AgentID, Title: t.Title, Lifecycle: api.WorkTaskLifecycle(t.Lifecycle), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

func workTaskEntry(e sqlc.WorkTaskEntry) api.WorkTaskEntry {
	out := api.WorkTaskEntry{Id: e.ID, Author: api.WorkTaskEntryAuthor(cmp.Or(e.Author, "unknown")), Content: e.Content, CreatedAt: e.CreatedAt}
	if e.EventID.Valid {
		out.EventId = new(e.EventID.String)
		out.Author = api.WorkTaskEntryAuthorExternal
	}
	return out
}

func workTaskError(err error) error {
	if errors.Is(err, worktask.ErrInvalid) {
		return errors.Join(errInvalid, err)
	}
	return err
}

func (h *handler) ListWorkTasks(ctx context.Context, req api.ListWorkTasksRequestObject) (api.ListWorkTasksResponseObject, error) {
	rows, err := h.WorkTasks.List(ctx, req.Params.IncludeClosed != nil && *req.Params.IncludeClosed)
	if err != nil {
		return nil, err
	}
	out := make([]api.WorkTask, 0, len(rows))
	for _, row := range rows {
		out = append(out, workTask(row))
	}
	return api.ListWorkTasks200JSONResponse{Tasks: out}, nil
}

func (h *handler) CreateWorkTask(ctx context.Context, req api.CreateWorkTaskRequestObject) (api.CreateWorkTaskResponseObject, error) {
	row, err := h.WorkTasks.Create(ctx, req.AgentId, req.Body.Title)
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.CreateWorkTask201JSONResponse(workTask(row)), nil
}

func (h *handler) GetWorkTask(ctx context.Context, req api.GetWorkTaskRequestObject) (api.GetWorkTaskResponseObject, error) {
	detail, err := h.WorkTasks.Detail(ctx, req.AgentId, req.TaskId)
	if err != nil {
		return nil, err
	}
	entries := make([]api.WorkTaskEntry, 0, len(detail.Entries))
	for _, entry := range detail.Entries {
		entries = append(entries, workTaskEntry(entry))
	}
	questions := make([]api.WorkTaskQuestion, 0, len(detail.Questions))
	for _, question := range detail.Questions {
		var options []string
		if err := json.Unmarshal([]byte(question.OptionsJson), &options); err != nil {
			return nil, err
		}
		questions = append(questions, api.WorkTaskQuestion{Id: question.ID, Text: question.Text, Options: options, Queued: question.Queued != 0})
	}
	out := api.GetWorkTask200JSONResponse{Task: workTask(detail.Task), Entries: entries, Questions: questions, Links: taskLinks(detail), Sessions: make([]api.WorkTaskSessionLink, 0, len(detail.Sessions))}
	for _, s := range detail.Sessions {
		out.Sessions = append(out.Sessions, api.WorkTaskSessionLink{
			ConversationId: s.ConversationID, AgentId: s.AgentID, AgentName: s.AgentName, Title: s.Title, IsPrimary: s.IsPrimary == 1, LinkedAt: s.LinkedAt,
		})
		if s.IsPrimary == 1 {
			out.Session = &api.WorkTaskSession{Id: s.ConversationID, AgentId: s.AgentID, Title: s.Title}
		}
	}
	return out, nil
}

func (h *handler) SetWorkTaskLifecycle(ctx context.Context, req api.SetWorkTaskLifecycleRequestObject) (api.SetWorkTaskLifecycleResponseObject, error) {
	row, err := h.WorkTasks.SetLifecycle(ctx, req.AgentId, req.TaskId, string(req.Body.Lifecycle))
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.SetWorkTaskLifecycle200JSONResponse(workTask(row)), nil
}

func (h *handler) AppendWorkTaskNote(ctx context.Context, req api.AppendWorkTaskNoteRequestObject) (api.AppendWorkTaskNoteResponseObject, error) {
	row, err := h.WorkTasks.Append(ctx, req.AgentId, req.TaskId, req.Body.Content)
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.AppendWorkTaskNote201JSONResponse(workTaskEntry(row)), nil
}

// taskLinks groups the waits of a Task under the objects they watch.
func taskLinks(detail worktask.Detail) []api.WorkTaskLink {
	type key struct{ namespace, objectType, object string }
	waits := make(map[key][]api.WorkTaskWait)
	for _, w := range detail.Waits {
		k := key{w.Namespace, w.ObjectType, w.ObjectKey}
		waits[k] = append(waits[k], api.WorkTaskWait{Id: w.ID, Event: w.EventType, Revision: w.Revision, AfterAt: w.AfterAt, Resolved: w.ResolvedBy.Valid, Alert: w.Alert == 1})
	}
	links := make([]api.WorkTaskLink, 0, len(detail.Objects))
	for _, o := range detail.Objects {
		link := api.WorkTaskLink{Namespace: o.Namespace, Type: o.ObjectType, Key: o.ObjectKey, Waits: waits[key{o.Namespace, o.ObjectType, o.ObjectKey}]}
		if link.Waits == nil {
			link.Waits = []api.WorkTaskWait{}
		}
		links = append(links, link)
	}
	return links
}
