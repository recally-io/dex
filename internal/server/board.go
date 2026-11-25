package server

import (
	"context"
	"encoding/json/v2"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worktask"
)

func (h *handler) ListBoardTasks(ctx context.Context, req api.ListBoardTasksRequestObject) (api.ListBoardTasksResponseObject, error) {
	p := req.Params
	rows, err := h.WorkTasks.ListBoard(ctx, worktask.BoardFilter{
		BotID: deref(p.BotId), Status: stringValue(p.Status), Source: deref(p.Source),
		IncludeClosed: p.IncludeClosed != nil && *p.IncludeClosed, Blocked: p.Blocked, InReview: p.InReview,
	})
	if err != nil {
		return nil, workTaskError(err)
	}
	out := api.ListBoardTasks200JSONResponse{Tasks: make([]api.BoardTaskSummary, len(rows))}
	for i, row := range rows {
		out.Tasks[i] = boardSummary(row)
	}
	return out, nil
}

func (h *handler) CreateBoardTask(ctx context.Context, req api.CreateBoardTaskRequestObject) (api.CreateBoardTaskResponseObject, error) {
	b := req.Body
	if b == nil {
		return nil, errInvalid
	}
	row, err := h.WorkTasks.CreateBoardTask(ctx, worktask.CreateTaskParams{
		AssigneeBotID: b.AssigneeBotId, Title: b.Title, Status: stringValue(b.Status), Note: deref(b.Note),
		ReviewerKind: stringValue(b.ReviewerKind), ReviewerID: deref(b.ReviewerId),
	})
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.CreateBoardTask201JSONResponse(boardSummary(row)), nil
}

func (h *handler) GetBoardTask(ctx context.Context, req api.GetBoardTaskRequestObject) (api.GetBoardTaskResponseObject, error) {
	detail, err := h.WorkTasks.BoardDetail(ctx, req.TaskKey)
	if err != nil {
		return nil, err
	}
	out := api.GetBoardTask200JSONResponse{
		Task:      boardSummary(worktask.NewBoardSummary(detail.Task, len(detail.Blockers), detail.Sessions)),
		Entries:   make([]api.BoardTaskEntry, len(detail.Entries)),
		Sessions:  make([]api.BoardTaskSession, len(detail.Sessions)),
		Questions: make([]api.BoardTaskQuestion, len(detail.Questions)),
		Blockers:  make([]api.BoardTaskBlocker, len(detail.Blockers)), Links: taskLinks(detail),
	}
	for i, e := range detail.Entries {
		entry, err := boardEntry(e)
		if err != nil {
			return nil, err
		}
		out.Entries[i] = entry
	}
	for i, s := range detail.Sessions {
		out.Sessions[i] = boardSession(s)
	}
	for i, b := range detail.Blockers {
		out.Blockers[i] = api.BoardTaskBlocker{
			Kind: api.BoardTaskBlockerKind(b.Kind), Namespace: optional(b.Namespace), ObjectType: optional(b.ObjectType),
			ObjectKey: optional(b.ObjectKey), EventType: optional(b.EventType), QuestionId: optional(b.QuestionID),
			Question: optional(b.Question), WaitId: nonzero(b.WaitID), QueuePosition: nonzero(b.QueuePosition),
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
		out.Questions[i] = api.BoardTaskQuestion{
			Id: q.ID, Text: q.Text, Options: options, Queued: q.Queued != 0, AgentId: q.AgentID, ConversationId: q.ConversationID,
		}
	}
	return out, nil
}

func (h *handler) UpdateBoardTask(ctx context.Context, req api.UpdateBoardTaskRequestObject) (api.UpdateBoardTaskResponseObject, error) {
	b, rev := req.Body, req.Params.IfMatch
	if b == nil || rev != nil && *rev < 0 {
		return nil, errInvalid
	}
	row, err := h.WorkTasks.UpdateBoardTask(ctx, req.TaskKey, worktask.BoardUpdate{
		Title: b.Title, Status: (*string)(b.Status), AssigneeBotID: b.AssigneeBotId,
		ReviewerKind: (*string)(b.ReviewerKind), ReviewerID: b.ReviewerId, Revision: rev,
	})
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.UpdateBoardTask200JSONResponse(boardSummary(row)), nil
}

func (h *handler) CreateBoardEntry(ctx context.Context, req api.CreateBoardEntryRequestObject) (api.CreateBoardEntryResponseObject, error) {
	b, rev := req.Body, req.Params.IfMatch
	if b == nil || rev != nil && *rev < 0 {
		return nil, errInvalid
	}
	entry, err := h.WorkTasks.CreateBoardEntry(ctx, req.TaskKey, rev, worktask.PostEntryParams{
		Kind: stringValue(b.Kind), Tag: stringValue(b.Tag), Content: b.Content,
	})
	if err != nil {
		return nil, workTaskError(err)
	}
	out, err := boardEntry(entry)
	return api.CreateBoardEntry201JSONResponse(out), err
}

func (h *handler) ReviewBoardTask(ctx context.Context, req api.ReviewBoardTaskRequestObject) (api.ReviewBoardTaskResponseObject, error) {
	b := req.Body
	if b == nil {
		return nil, errInvalid
	}
	var seq int64
	if b.ReviewSeq != nil {
		seq = *b.ReviewSeq
	}
	row, err := h.WorkTasks.ReviewBoardTask(ctx, req.TaskKey, string(b.Decision), seq, deref(b.Note))
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.ReviewBoardTask200JSONResponse(boardSummary(row)), nil
}

func boardSummary(row worktask.BoardSummary) api.BoardTaskSummary {
	t := row.Task
	out := api.BoardTaskSummary{
		Id: t.ID, Key: worktask.FormatKey(t.Number), Number: t.Number, Title: t.Title, Status: api.BoardStatus(t.Status),
		AssigneeBotId: t.AssigneeBotID, ReviewerKind: api.BoardReviewerKind(t.ReviewerKind), ReviewerId: t.ReviewerID,
		ReviewSeq: t.ReviewSeq, Revision: t.Revision, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Blocked: row.BlockerCount > 0, BlockerCount: int64(row.BlockerCount),
	}
	if row.PrimarySession != nil {
		out.PrimarySession = new(boardSession(*row.PrimarySession))
	}
	return out
}

func boardSession(s sqlc.ListWorkTaskSessionsRow) api.BoardTaskSession {
	out := api.BoardTaskSession{
		ConversationId: s.ConversationID, AgentId: s.AgentID, AgentName: s.AgentName, Title: s.Title,
		IsPrimary: s.IsPrimary == 1, LinkedAt: s.LinkedAt, State: api.BoardTaskSessionState(s.State),
	}
	if s.EndedAt.Valid {
		out.EndedAt = new(s.EndedAt.Int64)
	}
	return out
}

func boardEntry(e sqlc.WorkTaskEntry) (api.BoardTaskEntry, error) {
	out := api.BoardTaskEntry{
		Id: e.ID, Seq: e.Seq, Kind: api.BoardTaskEntryKind(e.Kind), Tag: e.Tag, ActorKind: e.ActorKind,
		ActorBotId: e.ActorBotID, Trust: api.BoardTaskEntryTrust(e.Trust), Content: e.Content, CreatedAt: e.CreatedAt,
	}
	if e.EventID.Valid {
		out.EventId = new(e.EventID.String)
	}
	if worktask.LegacyAuthor(e.Trust, e.MetaJson) == "unknown" {
		out.ActorKind = "unknown"
	}
	if e.Kind == worktask.KindStatus || e.Kind == worktask.KindReview {
		var meta struct {
			Status   *api.BoardStatus            `json:"status"`
			Decision *api.BoardTaskEntryDecision `json:"decision"`
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
