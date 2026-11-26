package server

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/settings"
)

func (h *handler) ListRunners(ctx context.Context, _ api.ListRunnersRequestObject) (api.ListRunnersResponseObject, error) {
	envs, err := h.Settings.ListRunners(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Runner, len(envs))
	for i, env := range envs {
		view, viewErr := h.runnerView(env)
		if viewErr != nil {
			return nil, viewErr
		}
		out[i] = view
	}
	return api.ListRunners200JSONResponse{Runners: out}, nil
}

func (h *handler) CreateRunner(ctx context.Context, req api.CreateRunnerRequestObject) (api.CreateRunnerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	in, err := runnerInput(*req.Body)
	if err != nil {
		return nil, err
	}
	created, err := h.Settings.CreateRunner(ctx, in)
	if err != nil {
		return nil, err
	}
	view, err := h.runnerView(created)
	if err != nil {
		return nil, err
	}
	return api.CreateRunner201JSONResponse(view), nil
}

func (h *handler) UpdateRunner(ctx context.Context, req api.UpdateRunnerRequestObject) (api.UpdateRunnerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	in, err := runnerInput(*req.Body)
	if err != nil {
		return nil, err
	}
	updated, runtimeChanged, err := h.Settings.UpdateRunner(ctx, req.RunnerId, in)
	if err != nil {
		return nil, err
	}
	if runtimeChanged {
		h.runnerChanged(ctx, req.RunnerId)
	}
	view, err := h.runnerView(updated)
	if err != nil {
		return nil, err
	}
	return api.UpdateRunner200JSONResponse(view), nil
}

func (h *handler) DeleteRunner(ctx context.Context, req api.DeleteRunnerRequestObject) (api.DeleteRunnerResponseObject, error) {
	if err := h.Settings.DeleteRunner(ctx, req.RunnerId); err != nil {
		return nil, err
	}
	h.runnerChanged(ctx, req.RunnerId)
	return api.DeleteRunner200JSONResponse(ok), nil
}

func (h *handler) runnerChanged(ctx context.Context, id string) {
	if h.RunnerChanged != nil {
		h.RunnerChanged(ctx, id)
	}
}

func (h *handler) TestRunner(ctx context.Context, req api.TestRunnerRequestObject) (api.TestRunnerResponseObject, error) {
	res, err := h.Settings.TestRunner(ctx, req.RunnerId)
	if err != nil {
		return nil, err
	}
	return api.TestRunner200JSONResponse(runnerTestResultView(res)), nil
}

func (h *handler) ProbeRunner(ctx context.Context, req api.ProbeRunnerRequestObject) (api.ProbeRunnerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	rawConfig, err := encodeRunnerConfig(req.Body.Config)
	if err != nil {
		return nil, err
	}
	res, err := h.Settings.ProbeRunner(ctx, deref(req.Body.RunnerId), settings.RunnerInput{
		Kind:   string(req.Body.Kind),
		Config: rawConfig,
		Secret: deref(req.Body.Secret),
	})
	if err != nil {
		return nil, err
	}
	return api.ProbeRunner200JSONResponse(runnerTestResultView(res)), nil
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

func (h *handler) ForgetRunnerHostKey(ctx context.Context, req api.ForgetRunnerHostKeyRequestObject) (api.ForgetRunnerHostKeyResponseObject, error) {
	if err := h.Settings.ForgetRunnerHostKey(ctx, req.RunnerId); err != nil {
		return nil, err
	}
	return api.ForgetRunnerHostKey200JSONResponse(ok), nil
}

func (h *handler) ResetBotRunner(ctx context.Context, _ api.ResetBotRunnerRequestObject) (api.ResetBotRunnerResponseObject, error) {
	id := botID(ctx)
	canReset, err := h.Envs.CanResetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canReset {
		return nil, fmt.Errorf("%w: environment reset applies only to disposable sandboxes", settings.ErrInvalid)
	}
	if err := h.Sessions.ResetRunner(ctx, id, func(rctx context.Context) error {
		return h.Envs.ResetBot(rctx, id)
	}); err != nil {
		return nil, err
	}
	return api.ResetBotRunner200JSONResponse(ok), nil
}

func encodeRunnerConfig(cfg *api.RunnerConfig) ([]byte, error) {
	if cfg == nil {
		return nil, nil
	}
	return json.Marshal(cfg)
}

func runnerTestResultView(res runner.TestResult) api.RunnerTestResult {
	var warnings *[]string
	if len(res.Warnings) > 0 {
		copied := slices.Clone(res.Warnings)
		warnings = &copied
	}
	var hostKeyChanged *bool
	if res.HostKeyChanged {
		hostKeyChanged = new(true)
	}
	return api.RunnerTestResult{
		Ok:             res.OK,
		Platform:       optional(res.Platform),
		Arch:           optional(res.Arch),
		Home:           optional(res.Home),
		Warnings:       warnings,
		HostKeyChanged: hostKeyChanged,
		Error:          optional(res.Error),
	}
}

func runnerInput(body api.RunnerInput) (settings.RunnerInput, error) {
	rawConfig, err := encodeRunnerConfig(body.Config)
	if err != nil {
		return settings.RunnerInput{}, err
	}
	return settings.RunnerInput{
		Name:        body.Name,
		Description: body.Description,
		Kind:        string(body.Kind),
		Config:      rawConfig,
		Secret:      deref(body.Secret),
	}, nil
}

func (h *handler) runnerView(env settings.Runner) (api.Runner, error) {
	var cfg api.RunnerConfig
	if len(env.Config) > 0 {
		if err := json.Unmarshal(env.Config, &cfg); err != nil {
			return api.Runner{}, err
		}
	}
	out := api.Runner{
		Id:          env.ID,
		Name:        env.Name,
		Description: optional(env.Description),
		Kind:        api.RunnerKind(env.Kind),
		Config:      cfg,
		SecretSet:   env.SecretSet,
		CreatedAt:   env.CreatedAt,
		UpdatedAt:   env.UpdatedAt,
	}
	if env.Kind == runner.KindRunner && h.Runners != nil {
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
