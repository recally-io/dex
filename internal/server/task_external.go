package server

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strings"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db/sqlc"
	tasksvc "github.com/recally-io/dex/internal/task"
)

type taskBearerKey struct{}

func taskBearer(ctx context.Context) string {
	token, _ := ctx.Value(taskBearerKey{}).(string)
	return token
}

// Match only the exact scoped endpoints here. Their handlers check the token digest.
func taskExternalPath(r *http.Request) bool {
	switch r.URL.EscapedPath() {
	case "/api/tasks/external/task":
		return r.Method == http.MethodGet
	case "/api/tasks/external/entries", "/api/tasks/external/heartbeat", "/api/tasks/external/finish":
		return r.Method == http.MethodPost
	default:
		return false
	}
}

func externalAttachParams(b *api.TaskExecutionAttach) tasksvc.ExternalParams {
	if b == nil {
		return tasksvc.ExternalParams{}
	}
	var host string
	if b.HostMeta != nil {
		host = deref(b.HostMeta.Hostname)
	}
	return tasksvc.ExternalParams{Harness: stringValue(b.Harness), MachineID: deref(b.MachineId), Hostname: host,
		WorkspacePath: deref(b.WorkspacePath), GitBranch: deref(b.GitBranch), ExternalSessionID: deref(b.ExternalSessionId), LeaseSeconds: int64Value(b.LeaseSeconds)}
}

func int64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func (h *handler) AttachTaskExecution(ctx context.Context, req api.AttachTaskExecutionRequestObject) (api.AttachTaskExecutionResponseObject, error) {
	issue, err := h.Tasks.AttachExternalExecution(ctx, tasksvc.Owner, tasksvc.OriginUser, req.TaskKey, externalAttachParams(req.Body))
	if err != nil {
		return nil, taskError(err)
	}
	return api.AttachTaskExecution201JSONResponse{Execution: taskExecution(issue.Execution), TaskToken: issue.TaskToken}, nil
}

func (h *handler) ResumeTaskExecution(ctx context.Context, req api.ResumeTaskExecutionRequestObject) (api.ResumeTaskExecutionResponseObject, error) {
	issue, err := h.Tasks.ResumeExternalExecution(ctx, tasksvc.Owner, tasksvc.OriginUser, req.ExecutionId, externalAttachParams(req.Body))
	if err != nil {
		return nil, taskError(err)
	}
	return api.ResumeTaskExecution200JSONResponse{Execution: taskExecution(issue.Execution), TaskToken: issue.TaskToken}, nil
}

func (h *handler) ListTaskExecutions(ctx context.Context, req api.ListTaskExecutionsRequestObject) (api.ListTaskExecutionsResponseObject, error) {
	p := req.Params
	rows, err := h.Tasks.ListTaskExecutions(ctx, deref(p.TaskKey), deref(p.MachineId), deref(p.Kind), deref(p.State))
	if err != nil {
		return nil, taskError(err)
	}
	out := api.ListTaskExecutions200JSONResponse{Executions: make([]api.TaskExecution, 0, len(rows))}
	for _, row := range rows {
		e := taskExecution(row.TaskExecution)
		e.TaskKey = new(tasksvc.FormatKey(row.TaskNumber))
		e.RunnerName = optional(row.RunnerName)
		if e.RunnerName != nil {
			e.MachineId = optional(row.TaskExecution.MachineID)
		}
		out.Executions = append(out.Executions, e)
	}
	return out, nil
}

func (h *handler) ReadTaskExternalTask(ctx context.Context, req api.ReadTaskExternalTaskRequestObject) (api.ReadTaskExternalTaskResponseObject, error) {
	after := int64Value(req.Params.AfterSeq)
	task, rows, err := h.Tasks.ExternalReadTask(ctx, taskBearer(ctx), after, int64Value(req.Params.Limit))
	if err != nil {
		return nil, taskError(err)
	}
	out := api.ReadTaskExternalTask200JSONResponse{Task: taskViewRef(task), Entries: make([]api.TaskEntry, 0, len(rows)), NextAfterSeq: after}
	for _, row := range rows {
		e, err := taskEntry(row)
		if err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, e)
		out.NextAfterSeq = row.Seq
	}
	return out, nil
}

func (h *handler) PostTaskExternalEntry(ctx context.Context, req api.PostTaskExternalEntryRequestObject) (api.PostTaskExternalEntryResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	row, err := h.Tasks.ExternalPostEntry(ctx, taskBearer(ctx), tasksvc.PostEntryParams{Kind: stringValue(b.Kind), Tag: stringValue(b.Tag), Content: b.Content})
	if err != nil {
		return nil, taskError(err)
	}
	out, err := taskEntry(row)
	return api.PostTaskExternalEntry201JSONResponse(out), err
}

func (h *handler) HeartbeatTaskExecution(ctx context.Context, req api.HeartbeatTaskExecutionRequestObject) (api.HeartbeatTaskExecutionResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	e, err := h.Tasks.ExternalHeartbeat(ctx, taskBearer(ctx), tasksvc.ExternalHeartbeat{LeaseSeconds: int64Value(b.LeaseSeconds), GitBranch: b.GitBranch, ExternalSessionID: b.ExternalSessionId})
	if err != nil {
		return nil, taskError(err)
	}
	return api.HeartbeatTaskExecution200JSONResponse(taskExecution(e)), nil
}

func (h *handler) FinishTaskExecution(ctx context.Context, req api.FinishTaskExecutionRequestObject) (api.FinishTaskExecutionResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	e, err := h.Tasks.ExternalFinish(ctx, taskBearer(ctx), string(req.Body.Outcome), deref(req.Body.Summary))
	if err != nil {
		return nil, taskError(err)
	}
	return api.FinishTaskExecution200JSONResponse(taskExecution(e)), nil
}

func taskExecution(e sqlc.TaskExecution) api.TaskExecution {
	var host api.TaskExecutionHost
	// The attach transaction writes the host object. Older Session rows have an empty object.
	_ = json.Unmarshal([]byte(e.HostMetaJson), &host)
	return api.TaskExecution{
		Id: e.ID, BotId: e.BotID, Kind: api.TaskExecutionKind(e.Kind), State: api.TaskExecutionState(e.State),
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

func taskTokenResponse(r *http.Request) bool {
	path := r.URL.Path
	return r.Method == http.MethodPost && (strings.HasPrefix(path, "/api/tasks/") && strings.HasSuffix(path, "/executions/attach") ||
		strings.HasPrefix(path, "/api/tasks/executions/") && strings.HasSuffix(path, "/resume"))
}
