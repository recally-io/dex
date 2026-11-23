package server

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/settings"
)

func (h *handler) ListEnvironments(ctx context.Context, _ api.ListEnvironmentsRequestObject) (api.ListEnvironmentsResponseObject, error) {
	envs, err := h.Settings.ListEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Environment, len(envs))
	for i, env := range envs {
		view, viewErr := h.environmentView(env)
		if viewErr != nil {
			return nil, viewErr
		}
		out[i] = view
	}
	return api.ListEnvironments200JSONResponse{Environments: out}, nil
}

func (h *handler) CreateEnvironment(ctx context.Context, req api.CreateEnvironmentRequestObject) (api.CreateEnvironmentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	in, err := environmentInput(*req.Body)
	if err != nil {
		return nil, err
	}
	created, err := h.Settings.CreateEnvironment(ctx, in)
	if err != nil {
		return nil, err
	}
	view, err := h.environmentView(created)
	if err != nil {
		return nil, err
	}
	return api.CreateEnvironment201JSONResponse(view), nil
}

func (h *handler) UpdateEnvironment(ctx context.Context, req api.UpdateEnvironmentRequestObject) (api.UpdateEnvironmentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	in, err := environmentInput(*req.Body)
	if err != nil {
		return nil, err
	}
	updated, err := h.Settings.UpdateEnvironment(ctx, req.EnvironmentId, in)
	if err != nil {
		return nil, err
	}
	h.environmentChanged(ctx, req.EnvironmentId)
	view, err := h.environmentView(updated)
	if err != nil {
		return nil, err
	}
	return api.UpdateEnvironment200JSONResponse(view), nil
}

func (h *handler) DeleteEnvironment(ctx context.Context, req api.DeleteEnvironmentRequestObject) (api.DeleteEnvironmentResponseObject, error) {
	if err := h.Settings.DeleteEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}
	h.environmentChanged(ctx, req.EnvironmentId)
	return api.DeleteEnvironment200JSONResponse(ok), nil
}

func (h *handler) environmentChanged(ctx context.Context, id string) {
	if h.EnvironmentChanged != nil {
		h.EnvironmentChanged(ctx, id)
	}
}

func (h *handler) TestEnvironment(ctx context.Context, req api.TestEnvironmentRequestObject) (api.TestEnvironmentResponseObject, error) {
	res, err := h.Settings.TestEnvironment(ctx, req.EnvironmentId)
	if err != nil {
		return nil, err
	}
	return api.TestEnvironment200JSONResponse(environmentTestResultView(res)), nil
}

func (h *handler) ProbeEnvironment(ctx context.Context, req api.ProbeEnvironmentRequestObject) (api.ProbeEnvironmentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	rawConfig, err := encodeEnvironmentConfig(req.Body.Config)
	if err != nil {
		return nil, err
	}
	res, err := h.Settings.ProbeEnvironment(ctx, deref(req.Body.EnvironmentId), settings.EnvironmentInput{
		Kind:   string(req.Body.Kind),
		Config: rawConfig,
		Secret: deref(req.Body.Secret),
	})
	if err != nil {
		return nil, err
	}
	return api.ProbeEnvironment200JSONResponse(environmentTestResultView(res)), nil
}

func (h *handler) DiscoverSshConfig(ctx context.Context, _ api.DiscoverSshConfigRequestObject) (api.DiscoverSshConfigResponseObject, error) { //nolint:revive // implements generated api.StrictServerInterface.
	disc, err := h.Settings.DiscoverSSHConfig(ctx)
	if err != nil {
		return nil, err
	}
	hosts := make([]api.SshHostEntry, 0, len(disc.Hosts))
	for _, entry := range disc.Hosts {
		var port *int
		if entry.Port > 0 {
			port = new(entry.Port)
		}
		hosts = append(hosts, api.SshHostEntry{
			Alias:        entry.Alias,
			HostName:     optional(entry.HostName),
			User:         optional(entry.User),
			Port:         port,
			IdentityFile: optional(entry.IdentityFile),
			ProxyJump:    optional(entry.ProxyJump),
			Source:       optional(entry.Source),
		})
	}
	files := slices.Clone(disc.IdentityFiles)
	if files == nil {
		files = []string{}
	}
	return api.DiscoverSshConfig200JSONResponse{
		Hosts:         hosts,
		IdentityFiles: files,
	}, nil
}

func (h *handler) ForgetEnvironmentHostKey(ctx context.Context, req api.ForgetEnvironmentHostKeyRequestObject) (api.ForgetEnvironmentHostKeyResponseObject, error) {
	if err := h.Settings.ForgetEnvironmentHostKey(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}
	return api.ForgetEnvironmentHostKey200JSONResponse(ok), nil
}

func (h *handler) ResetAgentEnvironment(ctx context.Context, _ api.ResetAgentEnvironmentRequestObject) (api.ResetAgentEnvironmentResponseObject, error) {
	id := agentID(ctx)
	canReset, err := h.Envs.CanResetAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canReset {
		return nil, fmt.Errorf("%w: environment reset applies only to disposable sandboxes", settings.ErrInvalid)
	}
	if err := h.Chats.ResetEnvironment(ctx, id, func(rctx context.Context) error {
		return h.Envs.ResetAgent(rctx, id)
	}); err != nil {
		return nil, err
	}
	return api.ResetAgentEnvironment200JSONResponse(ok), nil
}

func encodeEnvironmentConfig(cfg *api.EnvironmentConfig) ([]byte, error) {
	if cfg == nil {
		return nil, nil
	}
	return json.Marshal(cfg)
}

func environmentTestResultView(res execenv.TestResult) api.EnvironmentTestResult {
	var warnings *[]string
	if len(res.Warnings) > 0 {
		copied := slices.Clone(res.Warnings)
		warnings = &copied
	}
	var hostKeyChanged *bool
	if res.HostKeyChanged {
		hostKeyChanged = new(true)
	}
	return api.EnvironmentTestResult{
		Ok:             res.OK,
		Platform:       optional(res.Platform),
		Arch:           optional(res.Arch),
		Home:           optional(res.Home),
		Warnings:       warnings,
		HostKeyChanged: hostKeyChanged,
		Error:          optional(res.Error),
	}
}

func environmentInput(body api.EnvironmentInput) (settings.EnvironmentInput, error) {
	rawConfig, err := encodeEnvironmentConfig(body.Config)
	if err != nil {
		return settings.EnvironmentInput{}, err
	}
	return settings.EnvironmentInput{
		Name:   body.Name,
		Kind:   string(body.Kind),
		Config: rawConfig,
		Secret: deref(body.Secret),
	}, nil
}

func (h *handler) environmentView(env settings.Environment) (api.Environment, error) {
	var cfg api.EnvironmentConfig
	if len(env.Config) > 0 {
		if err := json.Unmarshal(env.Config, &cfg); err != nil {
			return api.Environment{}, err
		}
	}
	out := api.Environment{
		Id:        env.ID,
		Name:      env.Name,
		Kind:      api.EnvironmentKind(env.Kind),
		Config:    cfg,
		SecretSet: env.SecretSet,
		CreatedAt: env.CreatedAt,
		UpdatedAt: env.UpdatedAt,
	}
	if env.Kind == execenv.KindRunner && h.Runners != nil {
		st := h.Runners.Status(env.ID)
		runnerStatus := api.RunnerStatus{
			Online:           st.Online,
			Os:               optional(st.OS),
			Arch:             optional(st.Arch),
			Version:          optional(st.Version),
			SelfUpdateReason: optional(st.SelfUpdateReason),
		}
		if st.Online {
			runnerStatus.CanSelfUpdate = new(st.CanSelfUpdate)
		}
		out.Runner = &runnerStatus
	}
	return out, nil
}
