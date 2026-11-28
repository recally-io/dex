package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/upload"
)

// The longest message, in characters, and the most attachments of one message, as in the contract.
const (
	maxMessage     = 32000
	maxAttachments = 10
)

// errReadOnly is a message to a session that the web UI cannot write to.
var errReadOnly = errors.New("this session is read-only; send messages in Main, a separate Web session, or a child session")

func sessionView(c session.Session) api.Session {
	out := api.Session{
		Id: c.ID, Channel: api.SessionChannelWeb, Title: c.Title, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Busy: c.Busy,
		Cwd: c.Cwd, RunnerId: optional(c.RunnerID), Settings: c.Settings, Main: c.ChannelKey == session.MainKey, ReadOnly: !c.Writable,
	}
	if strings.HasPrefix(c.ChannelKey, "telegram:") {
		out.Channel = api.SessionChannelTelegram
	}
	if strings.HasPrefix(c.ChannelKey, "wechat:") {
		out.Channel = api.SessionChannelWechat
	}
	if strings.HasPrefix(c.ChannelKey, "feishu:") {
		out.Channel = api.SessionChannelFeishu
	}
	if id, ok := automation.SessionAutomation(c.ChannelKey); ok {
		out.Channel, out.AutomationId = api.SessionChannelAutomation, &id
	}
	if c.ArchivedAt != 0 {
		out.ArchivedAt = &c.ArchivedAt
	}
	if c.ParentID != 0 {
		out.ParentId = &c.ParentID
	}
	if c.StoppedByParent {
		out.StoppedWithParent = &c.StoppedByParent
	}
	if c.BotID != "" {
		out.BotId, out.BotName = &c.BotID, &c.BotName
	}
	if c.ParentBotID != "" {
		out.ParentBotId, out.ParentBotName = &c.ParentBotID, &c.ParentBotName
	}
	if o := c.ForkedFrom; o != nil {
		out.ForkedFrom = &api.SessionOrigin{Id: o.ID, Title: o.Title, Main: o.Main}
	}
	if q := c.Question; q != nil {
		out.Question = &api.SessionQuestion{Id: q.ID, Text: q.Text, Options: q.Options, Status: api.SessionQuestionStatus(q.Status)}
		if q.Answer != "" {
			out.Question.Answer = &q.Answer
		}
		if q.Action != "" {
			action := api.SessionQuestionAction(q.Action)
			out.Question.Action = &action
		}
		if q.Outcome != "" {
			out.Question.Outcome = &q.Outcome
		}
	}
	return out
}

func (h *handler) ArchiveSession(ctx context.Context, req api.ArchiveSessionRequestObject) (api.ArchiveSessionResponseObject, error) {
	c, err := h.Sessions.SetArchived(ctx, botID(ctx), req.SessionId, true)
	if err != nil {
		return nil, err
	}
	return api.ArchiveSession200JSONResponse(sessionView(c)), nil
}

func (h *handler) UnarchiveSession(ctx context.Context, req api.UnarchiveSessionRequestObject) (api.UnarchiveSessionResponseObject, error) {
	c, err := h.Sessions.SetArchived(ctx, botID(ctx), req.SessionId, false)
	if err != nil {
		return nil, err
	}
	return api.UnarchiveSession200JSONResponse(sessionView(c)), nil
}

func (h *handler) ListSessions(ctx context.Context, _ api.ListSessionsRequestObject) (api.ListSessionsResponseObject, error) {
	return h.listSessions(ctx, botID(ctx))
}

func (h *handler) listSessions(ctx context.Context, botID string) (api.ListSessions200JSONResponse, error) {
	sessions, err := h.Sessions.List(ctx, botID)
	if err != nil {
		return api.ListSessions200JSONResponse{}, err
	}
	out := api.ListSessions200JSONResponse{Sessions: make([]api.Session, len(sessions))}
	// The list has no execution ID. Only a busy session needs one, to tell the web UI that it stalled.
	var refs []engine.Ref
	refOf := map[int]engine.Ref{}
	for i, c := range sessions {
		out.Sessions[i] = sessionView(c)
		if !c.Busy {
			continue
		}
		// A child in another bot belongs to that bot.
		owner := cmp.Or(c.BotID, botID)
		full, err := h.Sessions.Get(ctx, owner, c.ID)
		if err != nil {
			return api.ListSessions200JSONResponse{}, err
		}
		if full.ExecutionID > 0 {
			refOf[i] = engine.Ref{BotID: owner, ID: full.ExecutionID}
			refs = append(refs, refOf[i])
		}
	}
	activities := h.Engine.Activity(refs)
	for i, ref := range refOf {
		out.Sessions[i].LastActivityAt = activities[ref].LastActivityAt
		out.Sessions[i].ToolStartedAt = activities[ref].ToolStartedAt
	}
	return out, nil
}

func (h *handler) GetSession(ctx context.Context, req api.GetSessionRequestObject) (api.GetSessionResponseObject, error) {
	c, err := h.Sessions.Get(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.GetSession200JSONResponse(sessionView(c)), nil
}

// message validates a message from the client. The text can be empty when the message has an
// attachment.
func (h *handler) message(ctx context.Context, in api.MessageInput) (session.Message, error) {
	id, err := uuid.Parse(in.Id)
	if err != nil {
		return session.Message{}, errors.Join(errInvalid, errors.New("the message ID must be a UUID"))
	}
	var ids []string
	if in.Attachments != nil {
		ids = *in.Attachments
	}
	if len(ids) > maxAttachments {
		return session.Message{}, errors.Join(errInvalid, errors.New("a message can have at most 10 attachments"))
	}
	m := session.Message{ID: id.String(), Content: session.NormalizeText(in.Text)}
	if in.TaskId != nil {
		m.TaskID = *in.TaskId
	}
	if in.AnswersQuestionId != nil {
		m.AnswersQuestion = *in.AnswersQuestionId
	}
	if in.Mode != nil {
		if !in.Mode.Valid() {
			return session.Message{}, errors.Join(errInvalid, errors.New("the mode must be followUp, steer or reject"))
		}
		m.WhenBusy = string(*in.Mode)
	}
	if (m.Content == "" && len(ids) == 0) || utf8.RuneCountInString(m.Content) > maxMessage {
		return session.Message{}, errors.Join(errInvalid, errors.New("the message must have 1 to 32000 characters"))
	}
	for i, uploadID := range ids {
		if slices.Contains(ids[:i], uploadID) {
			return session.Message{}, errors.Join(errInvalid, fmt.Errorf("the attachment %q is in the message twice", uploadID))
		}
		a, err := h.uploads(ctx).Get(uploadID)
		if errors.Is(err, upload.ErrNotFound) {
			return session.Message{}, errors.Join(errInvalid, fmt.Errorf("no upload %q; upload the file again", uploadID))
		}
		if err != nil {
			return session.Message{}, err
		}
		m.Attachments = append(m.Attachments, a)
	}
	return m, nil
}

func (h *handler) CreateBotSession(ctx context.Context, _ api.CreateBotSessionRequestObject) (api.CreateBotSessionResponseObject, error) {
	c, err := h.Sessions.Create(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.CreateBotSession201JSONResponse(sessionView(c)), nil
}

func (h *handler) SendMessage(ctx context.Context, req api.SendMessageRequestObject) (api.SendMessageResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	m, err := h.message(ctx, *req.Body)
	if err != nil {
		return nil, err
	}
	c, err := h.writableSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if _, err := h.Sessions.Send(ctx, botID(ctx), c.ChannelKey, m); err != nil {
		return nil, err
	}
	return api.SendMessage202JSONResponse{SubmissionId: m.ID}, nil
}

func (h *handler) WithdrawMessage(ctx context.Context, req api.WithdrawMessageRequestObject) (api.WithdrawMessageResponseObject, error) {
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	m, err := h.Sessions.Withdraw(ctx, botID(ctx), req.SessionId, req.MessageId)
	if err != nil {
		return nil, err
	}
	out := api.WithdrawMessage200JSONResponse{Id: m.ID, Text: m.Content, Attachments: m.Attachments}
	if out.Attachments == nil {
		out.Attachments = []api.Attachment{}
	}
	return out, nil
}

// writableSession returns session id, or errReadOnly for a session that the web UI cannot write to.
func (h *handler) writableSession(ctx context.Context, id int64) (session.Session, error) {
	c, err := h.Sessions.Get(ctx, botID(ctx), id)
	if err != nil {
		return session.Session{}, err
	}
	if !c.Writable {
		return session.Session{}, errReadOnly
	}
	return c, nil
}

func (h *handler) SetSessionProject(ctx context.Context, req api.SetSessionProjectRequestObject) (api.SetSessionProjectResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if _, err := h.Sessions.SetProject(ctx, botID(ctx), req.SessionId, req.Body.Cwd); err != nil {
		return nil, err
	}
	c, err := h.Sessions.Get(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.SetSessionProject200JSONResponse(sessionView(c)), nil
}

func (h *handler) SetSessionRunner(ctx context.Context, req api.SetSessionRunnerRequestObject) (api.SetSessionRunnerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	c, err := h.Sessions.SetRunner(ctx, botID(ctx), req.SessionId, strings.TrimSpace(req.Body.RunnerId))
	if err != nil {
		return nil, err
	}
	return api.SetSessionRunner200JSONResponse(sessionView(c)), nil
}

func (h *handler) SetSessionSettings(ctx context.Context, req api.SetSessionSettingsRequestObject) (api.SetSessionSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Sessions.SetSession(ctx, botID(ctx), req.SessionId, *req.Body); err != nil {
		return nil, err
	}
	c, err := h.Sessions.Get(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.SetSessionSettings200JSONResponse(sessionView(c)), nil
}

func (h *handler) AbortSession(ctx context.Context, req api.AbortSessionRequestObject) (api.AbortSessionResponseObject, error) {
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	if err := h.Sessions.Stop(ctx, botID(ctx), req.SessionId, req.Params.RunTarget); err != nil {
		return nil, err
	}
	return api.AbortSession200JSONResponse{Status: api.OkStatusOk}, nil
}

func (h *handler) ListSessionMonitors(ctx context.Context, req api.ListSessionMonitorsRequestObject) (api.ListSessionMonitorsResponseObject, error) {
	monitors, err := h.Sessions.ListMonitors(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	out := make([]api.Monitor, len(monitors))
	for i, t := range monitors {
		out[i] = monitorResponse(t)
	}
	return api.ListSessionMonitors200JSONResponse{Monitors: out}, nil
}

func (h *handler) CancelSessionMonitor(ctx context.Context, req api.CancelSessionMonitorRequestObject) (api.CancelSessionMonitorResponseObject, error) {
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	t, err := h.Sessions.CancelMonitor(ctx, botID(ctx), req.SessionId, req.MonitorId)
	if err != nil {
		return nil, err
	}
	return api.CancelSessionMonitor200JSONResponse(monitorResponse(t)), nil
}

func monitorResponse(t session.MonitorInfo) api.Monitor {
	// This endpoint serves schedules only and keeps their existing IDs.
	return api.Monitor{Id: strings.TrimPrefix(t.ID, "schedule-"), DueAt: t.DueAt, TimeZone: t.TimeZone, Prompt: t.Prompt, State: api.MonitorState(t.State), ChainDepth: t.ChainDepth, CreatedAt: t.CreatedAt, Mode: api.MonitorMode(t.Mode), MaxRuns: t.MaxRuns, Runs: t.Runs, ExpiresAt: t.ExpiresAt, Outcome: t.Outcome}
}

func (h *handler) ListSessionOutbox(ctx context.Context, req api.ListSessionOutboxRequestObject) (api.ListSessionOutboxResponseObject, error) {
	rows, err := h.Sessions.ListOutbox(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	out := make([]api.OutboxMessage, len(rows))
	for i, r := range rows {
		out[i] = api.OutboxMessage{
			Id:          r.ID,
			Kind:        api.OutboxMessageKind(r.Kind),
			Peer:        r.Peer,
			TopicKey:    r.TopicKey,
			Body:        r.Body,
			Reason:      r.Reason,
			Status:      api.OutboxMessageStatus(r.Status),
			Error:       r.Error,
			RetryAt:     r.RetryAt,
			CreatedAt:   r.CreatedAt,
			DeliveredAt: r.DeliveredAt,
		}
	}
	return api.ListSessionOutbox200JSONResponse{Messages: out}, nil
}

func (h *handler) GetMainSession(ctx context.Context, _ api.GetMainSessionRequestObject) (api.GetMainSessionResponseObject, error) {
	c, err := h.Sessions.Main(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetMainSession200JSONResponse(sessionView(c)), nil
}

func (h *handler) ClearContext(ctx context.Context, req api.ClearContextRequestObject) (api.ClearContextResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	id, err := uuid.Parse(req.Body.Id)
	if err != nil {
		return nil, errors.Join(errInvalid, err)
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	handoff := strings.TrimSpace(deref(req.Body.Handoff))
	if utf8.RuneCountInString(handoff) > maxMessage {
		return nil, errors.Join(errInvalid, errors.New("the handoff must have at most 32000 characters"))
	}
	if err := h.Sessions.Clear(ctx, botID(ctx), req.SessionId, id.String(), "web", handoff); err != nil {
		return nil, err
	}
	return api.ClearContext202JSONResponse{SubmissionId: id.String()}, nil
}

func (h *handler) CompactSession(ctx context.Context, req api.CompactSessionRequestObject) (api.CompactSessionResponseObject, error) {
	instructions, requestID := "", ""
	if req.Body != nil {
		instructions = strings.TrimSpace(deref(req.Body.Instructions))
		if req.Body.Id != nil {
			id, err := uuid.Parse(*req.Body.Id)
			if err != nil {
				return nil, errors.Join(errInvalid, err)
			}
			requestID = id.String()
		}
	}
	if utf8.RuneCountInString(instructions) > maxMessage {
		return nil, errors.Join(errInvalid, errors.New("the instructions must have at most 32000 characters"))
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	if err := h.Sessions.Compact(ctx, botID(ctx), req.SessionId, instructions, requestID); err != nil {
		return nil, err
	}
	return api.CompactSession202Response{}, nil
}

func (h *handler) ForkSession(ctx context.Context, req api.ForkSessionRequestObject) (api.ForkSessionResponseObject, error) {
	if req.Body == nil || req.Body.EntryId <= 0 {
		return nil, errInvalid
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	requestID := ""
	if req.Body.Id != nil {
		id, err := uuid.Parse(*req.Body.Id)
		if err != nil {
			return nil, errors.Join(errInvalid, err)
		}
		requestID = id.String()
	}
	runnerID := ""
	if req.Body.RunnerId != nil {
		runnerID = *req.Body.RunnerId
	}
	fork, err := h.Sessions.Fork(ctx, botID(ctx), req.SessionId, req.Body.EntryId, req.Body.Before != nil && *req.Body.Before, requestID, runnerID)
	if err != nil {
		return nil, err
	}
	return api.ForkSession201JSONResponse{SessionId: fork.ID}, nil
}

func (h *handler) ContinueSession(ctx context.Context, req api.ContinueSessionRequestObject) (api.ContinueSessionResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	id, err := uuid.Parse(req.Body.Id)
	if err != nil {
		return nil, errors.Join(errInvalid, err)
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	if err := h.Sessions.Continue(ctx, botID(ctx), req.SessionId, id.String()); err != nil {
		return nil, err
	}
	return api.ContinueSession202JSONResponse{SubmissionId: id.String()}, nil
}
