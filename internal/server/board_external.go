package server

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strings"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worktask"
)

type boardBearerKey struct{}

func boardBearer(ctx context.Context) string {
	token, _ := ctx.Value(boardBearerKey{}).(string)
	return token
}

// Match only the exact scoped endpoints here. Their handlers check the token digest.
func boardExternalPath(r *http.Request) bool {
	switch r.URL.EscapedPath() {
	case "/api/board/external/task":
		return r.Method == http.MethodGet
	case "/api/board/external/entries", "/api/board/external/heartbeat", "/api/board/external/finish":
		return r.Method == http.MethodPost
	default:
		return false
	}
}

func boardAttachParams(b *api.BoardExecutionAttach) worktask.ExternalParams {
	if b == nil {
		return worktask.ExternalParams{}
	}
	var host string
	if b.HostMeta != nil {
		host = deref(b.HostMeta.Hostname)
	}
	return worktask.ExternalParams{Harness: stringValue(b.Harness), MachineID: deref(b.MachineId), Hostname: host,
		WorkspacePath: deref(b.WorkspacePath), GitBranch: deref(b.GitBranch), ExternalSessionID: deref(b.ExternalSessionId), LeaseSeconds: int64Value(b.LeaseSeconds)}
}

func int64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func (h *handler) AttachBoardExecution(ctx context.Context, req api.AttachBoardExecutionRequestObject) (api.AttachBoardExecutionResponseObject, error) {
	issue, err := h.WorkTasks.AttachExternalExecution(ctx, worktask.Owner, worktask.OriginUser, req.TaskKey, boardAttachParams(req.Body))
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.AttachBoardExecution201JSONResponse{Execution: boardExecution(issue.Execution), BoardToken: issue.BoardToken}, nil
}

func (h *handler) ResumeBoardExecution(ctx context.Context, req api.ResumeBoardExecutionRequestObject) (api.ResumeBoardExecutionResponseObject, error) {
	issue, err := h.WorkTasks.ResumeExternalExecution(ctx, worktask.Owner, worktask.OriginUser, req.ExecutionId, boardAttachParams(req.Body))
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.ResumeBoardExecution200JSONResponse{Execution: boardExecution(issue.Execution), BoardToken: issue.BoardToken}, nil
}

func (h *handler) ListBoardExecutions(ctx context.Context, req api.ListBoardExecutionsRequestObject) (api.ListBoardExecutionsResponseObject, error) {
	p := req.Params
	rows, err := h.WorkTasks.ListBoardExecutions(ctx, deref(p.TaskKey), deref(p.MachineId), deref(p.Kind), deref(p.State))
	if err != nil {
		return nil, workTaskError(err)
	}
	out := api.ListBoardExecutions200JSONResponse{Executions: make([]api.BoardExecution, 0, len(rows))}
	for _, row := range rows {
		e := boardExecution(row.TaskExecution)
		e.TaskKey = new(worktask.FormatKey(row.TaskNumber))
		e.RunnerName = optional(row.RunnerName)
		if e.RunnerName != nil {
			e.MachineId = optional(row.TaskExecution.MachineID)
		}
		out.Executions = append(out.Executions, e)
	}
	return out, nil
}

func (h *handler) ReadBoardExternalTask(ctx context.Context, req api.ReadBoardExternalTaskRequestObject) (api.ReadBoardExternalTaskResponseObject, error) {
	after := int64Value(req.Params.AfterSeq)
	task, rows, err := h.WorkTasks.ExternalReadTask(ctx, boardBearer(ctx), after, int64Value(req.Params.Limit))
	if err != nil {
		return nil, workTaskError(err)
	}
	out := api.ReadBoardExternalTask200JSONResponse{Task: boardTaskRef(task), Entries: make([]api.BoardTaskEntry, 0, len(rows)), NextAfterSeq: after}
	for _, row := range rows {
		e, err := boardEntry(row)
		if err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, e)
		out.NextAfterSeq = row.Seq
	}
	return out, nil
}

func (h *handler) PostBoardExternalEntry(ctx context.Context, req api.PostBoardExternalEntryRequestObject) (api.PostBoardExternalEntryResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	row, err := h.WorkTasks.ExternalPostEntry(ctx, boardBearer(ctx), worktask.PostEntryParams{Kind: stringValue(b.Kind), Tag: stringValue(b.Tag), Content: b.Content})
	if err != nil {
		return nil, workTaskError(err)
	}
	out, err := boardEntry(row)
	return api.PostBoardExternalEntry201JSONResponse(out), err
}

func (h *handler) HeartbeatBoardExecution(ctx context.Context, req api.HeartbeatBoardExecutionRequestObject) (api.HeartbeatBoardExecutionResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	e, err := h.WorkTasks.ExternalHeartbeat(ctx, boardBearer(ctx), worktask.ExternalHeartbeat{LeaseSeconds: int64Value(b.LeaseSeconds), GitBranch: b.GitBranch, ExternalSessionID: b.ExternalSessionId})
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.HeartbeatBoardExecution200JSONResponse(boardExecution(e)), nil
}

func (h *handler) FinishBoardExecution(ctx context.Context, req api.FinishBoardExecutionRequestObject) (api.FinishBoardExecutionResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	e, err := h.WorkTasks.ExternalFinish(ctx, boardBearer(ctx), string(req.Body.Outcome), deref(req.Body.Summary))
	if err != nil {
		return nil, workTaskError(err)
	}
	return api.FinishBoardExecution200JSONResponse(boardExecution(e)), nil
}

func boardExecution(e sqlc.TaskExecution) api.BoardExecution {
	var host api.BoardExecutionHost
	// The attach transaction writes the host object. Older Session rows have an empty object.
	_ = json.Unmarshal([]byte(e.HostMetaJson), &host)
	return api.BoardExecution{
		Id: e.ID, BotId: e.BotID, Kind: api.BoardExecutionKind(e.Kind), State: api.BoardExecutionState(e.State),
		Harness: optional(e.Harness), Hostname: host.Hostname, WorkspacePath: optional(e.WorkspacePath), GitBranch: optional(e.GitBranch),
		ExternalSessionId: optional(e.ExternalSessionID), DispatchId: optional(e.DispatchID), LeaseEpoch: e.LeaseEpoch,
		LeaseExpiresAt: nullExecutionTime(e.LeaseExpiresAt.Int64, e.LeaseExpiresAt.Valid), LastHeartbeatAt: nullExecutionTime(e.LastHeartbeatAt.Int64, e.LastHeartbeatAt.Valid),
		StartedAt: e.StartedAt, EndedAt: nullExecutionTime(e.EndedAt.Int64, e.EndedAt.Valid),
	}
}

func nullExecutionTime(value int64, valid bool) *int64 {
	if !valid {
		return nil
	}
	return &value
}

func boardTokenResponse(r *http.Request) bool {
	path := r.URL.Path
	return r.Method == http.MethodPost && (strings.HasPrefix(path, "/api/board/tasks/") && strings.HasSuffix(path, "/executions/attach") ||
		strings.HasPrefix(path, "/api/board/executions/") && strings.HasSuffix(path, "/resume"))
}
