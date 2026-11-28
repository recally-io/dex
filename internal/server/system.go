package server

import (
	"context"
	"net/http"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/host/sysops"
)

func systemStatusView(s sysops.Status) api.SystemStatus {
	out := api.SystemStatus{
		Version:         s.Version,
		StartedAt:       s.StartedAt,
		State:           api.SystemStatusState(s.State),
		UpdateChannel:   api.SystemUpdateChannel(s.UpdateChannel),
		UpdateAvailable: s.UpdateAvailable,
		Capability: api.SystemCapability{
			Supported:  s.Capability.Supported,
			CanRestart: s.Capability.CanRestart,
		},
		Backups: make([]api.SystemBackup, 0, len(s.Backups)),
	}
	if s.Capability.Reason != "" {
		out.Capability.Reason = new(s.Capability.Reason)
	}
	if s.PendingAction != "" {
		out.PendingAction = new(api.SystemStatusPendingAction(s.PendingAction))
	}
	if s.TargetVersion != "" {
		out.TargetVersion = new(s.TargetVersion)
	}
	if s.DownloadDone > 0 {
		out.DownloadDone = new(s.DownloadDone)
	}
	if s.DownloadTotal > 0 {
		out.DownloadTotal = new(s.DownloadTotal)
	}
	if s.Latest != nil {
		rel := api.SystemRelease{
			Tag:        s.Latest.Tag,
			Prerelease: s.Latest.Prerelease,
		}
		if s.Latest.PublishedAt != "" {
			rel.PublishedAt = new(s.Latest.PublishedAt)
		}
		if s.Latest.Notes != "" {
			rel.Notes = new(s.Latest.Notes)
		}
		if s.Latest.URL != "" {
			rel.Url = new(s.Latest.URL)
		}
		out.Latest = &rel
	}
	if s.LastCheckedAt > 0 {
		out.LastCheckedAt = new(s.LastCheckedAt)
	}
	if s.LastError != "" {
		out.LastError = new(s.LastError)
	}
	for _, b := range s.Backups {
		out.Backups = append(out.Backups, api.SystemBackup{
			Name:      b.Name,
			SizeBytes: b.SizeBytes,
			CreatedAt: b.CreatedAt,
		})
	}
	return out
}

func (h *handler) GetSystemStatus(ctx context.Context, _ api.GetSystemStatusRequestObject) (api.GetSystemStatusResponseObject, error) {
	if h.System == nil {
		return api.GetSystemStatusdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	st, err := h.System.Status(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetSystemStatus200JSONResponse(systemStatusView(st)), nil
}

func (h *handler) UpdateSystemSettings(ctx context.Context, req api.UpdateSystemSettingsRequestObject) (api.UpdateSystemSettingsResponseObject, error) {
	if h.System == nil {
		return api.UpdateSystemSettingsdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	st, err := h.System.SetChannel(ctx, string(req.Body.UpdateChannel))
	if err != nil {
		return nil, err
	}
	return api.UpdateSystemSettings200JSONResponse(systemStatusView(st)), nil
}

func (h *handler) CheckSystemUpdate(ctx context.Context, _ api.CheckSystemUpdateRequestObject) (api.CheckSystemUpdateResponseObject, error) {
	if h.System == nil {
		return api.CheckSystemUpdatedefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	st, err := h.System.CheckUpdate(ctx)
	if err != nil {
		return nil, err
	}
	return api.CheckSystemUpdate200JSONResponse(systemStatusView(st)), nil
}

func (h *handler) UpgradeSystem(ctx context.Context, req api.UpgradeSystemRequestObject) (api.UpgradeSystemResponseObject, error) {
	if h.System == nil {
		return api.UpgradeSystemdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	var target string
	var force bool
	if req.Body != nil {
		if req.Body.Version != nil {
			target = *req.Body.Version
		}
		if req.Body.Force != nil {
			force = *req.Body.Force
		}
	}
	st, err := h.System.Upgrade(ctx, target, force)
	if err != nil {
		return nil, err
	}
	return api.UpgradeSystem200JSONResponse(systemStatusView(st)), nil
}

func (h *handler) RestartSystem(ctx context.Context, req api.RestartSystemRequestObject) (api.RestartSystemResponseObject, error) {
	if h.System == nil {
		return api.RestartSystemdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	force := req.Body != nil && req.Body.Force != nil && *req.Body.Force
	st, err := h.System.Restart(ctx, force)
	if err != nil {
		return nil, err
	}
	return api.RestartSystem200JSONResponse(systemStatusView(st)), nil
}

func (h *handler) CancelSystemOperation(ctx context.Context, _ api.CancelSystemOperationRequestObject) (api.CancelSystemOperationResponseObject, error) {
	if h.System == nil {
		return api.CancelSystemOperationdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	st, err := h.System.CancelPending(ctx)
	if err != nil {
		return nil, err
	}
	return api.CancelSystemOperation200JSONResponse(systemStatusView(st)), nil
}

func (h *handler) BackupSystemDatabase(ctx context.Context, _ api.BackupSystemDatabaseRequestObject) (api.BackupSystemDatabaseResponseObject, error) {
	if h.System == nil {
		return api.BackupSystemDatabasedefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	b, err := h.System.Backup(ctx)
	if err != nil {
		return nil, err
	}
	return api.BackupSystemDatabase200JSONResponse{
		Name:      b.Name,
		SizeBytes: b.SizeBytes,
		CreatedAt: b.CreatedAt,
	}, nil
}

func (h *handler) GetSystemLogs(_ context.Context, req api.GetSystemLogsRequestObject) (api.GetSystemLogsResponseObject, error) {
	if h.System == nil {
		return api.GetSystemLogsdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "system operations are unavailable"}}, nil
	}
	limit := 200
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	entries := h.System.Logs(limit)
	out := make([]api.SystemLogEntry, 0, len(entries))
	for _, e := range entries {
		item := api.SystemLogEntry{
			Seq:     e.Seq,
			Time:    e.Time,
			Level:   e.Level,
			Message: e.Message,
		}
		if e.Attrs != "" {
			item.Attrs = new(e.Attrs)
		}
		out = append(out, item)
	}
	return api.GetSystemLogs200JSONResponse{Entries: out}, nil
}
