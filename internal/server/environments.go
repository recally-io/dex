package server

import (
	"context"
	"encoding/json/v2"
	"errors"
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
		out[i] = environmentView(env)
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
	return api.CreateEnvironment201JSONResponse(environmentView(created)), nil
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
	return api.UpdateEnvironment200JSONResponse(environmentView(updated)), nil
}

func (h *handler) DeleteEnvironment(ctx context.Context, req api.DeleteEnvironmentRequestObject) (api.DeleteEnvironmentResponseObject, error) {
	if err := h.Settings.DeleteEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}
	return api.DeleteEnvironment200JSONResponse(ok), nil
}

func (h *handler) TestEnvironment(ctx context.Context, req api.TestEnvironmentRequestObject) (api.TestEnvironmentResponseObject, error) {
	res, err := h.Settings.TestEnvironment(ctx, req.EnvironmentId)
	if err != nil {
		return nil, err
	}
	var warnings *[]string
	if len(res.Warnings) > 0 {
		copied := slices.Clone(res.Warnings)
		warnings = &copied
	}
	return api.TestEnvironment200JSONResponse{
		Ok:       res.OK,
		Platform: optional(res.Platform),
		Arch:     optional(res.Arch),
		Home:     optional(res.Home),
		Warnings: warnings,
		Error:    optional(res.Error),
	}, nil
}

func (h *handler) ResetAgentEnvironment(ctx context.Context, _ api.ResetAgentEnvironmentRequestObject) (api.ResetAgentEnvironmentResponseObject, error) {
	id := agentID(ctx)
	if h.Envs == nil {
		return nil, errors.New("execution environment manager is not configured")
	}
	kind, err := h.Envs.AgentEnvironmentKind(ctx, id)
	if err != nil {
		return nil, err
	}
	if kind != execenv.KindE2B {
		return nil, fmt.Errorf("%w: environment reset applies only to e2b sandboxes", settings.ErrInvalid)
	}
	if h.Chats != nil {
		if err := h.Chats.ResetEnvironment(ctx, id, func(rctx context.Context) error {
			return h.Envs.ResetAgent(rctx, id)
		}); err != nil {
			return nil, err
		}
	} else if err := h.Envs.ResetAgent(ctx, id); err != nil {
		return nil, err
	}
	return api.ResetAgentEnvironment200JSONResponse(ok), nil
}

func environmentInput(body api.EnvironmentInput) (settings.EnvironmentInput, error) {
	var rawConfig []byte
	if body.Config != nil {
		encoded, err := json.Marshal(body.Config)
		if err != nil {
			return settings.EnvironmentInput{}, err
		}
		rawConfig = encoded
	}
	return settings.EnvironmentInput{
		Name:   body.Name,
		Kind:   string(body.Kind),
		Config: rawConfig,
		Secret: deref(body.Secret),
	}, nil
}

func environmentView(env settings.Environment) api.Environment {
	return api.Environment{
		Id:   env.ID,
		Name: env.Name,
		Kind: api.EnvironmentKind(env.Kind),
		Config: api.EnvironmentConfig{
			Host:           optional(env.Config.Host),
			User:           optional(env.Config.User),
			Port:           env.Config.Port,
			IdentityFile:   optional(env.Config.IdentityFile),
			WorkspaceDir:   optional(env.Config.WorkspaceDir),
			Template:       optional(env.Config.Template),
			TimeoutSeconds: env.Config.TimeoutSeconds,
		},
		SecretSet: env.SecretSet,
		CreatedAt: env.CreatedAt,
		UpdatedAt: env.UpdatedAt,
	}
}
