package engine

import (
	"context"
	"errors"
	"slices"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	"github.com/recally-io/dex/internal/vault"
)

func (a *Bots) VaultList(botID string) ([]vault.Metadata, error) {
	if !botIDPattern.MatchString(botID) {
		return nil, ErrInvalidInput
	}
	snapshot, err := a.vault.Load(botID)
	return snapshot.All(), err
}

func (a *Bots) VaultEdit(botID, scope, name string, value *string) error {
	if !botIDPattern.MatchString(botID) {
		return ErrInvalidInput
	}
	err := a.vault.Edit(botID, scope, name, value)
	if errors.Is(err, vault.ErrInvalid) {
		return errors.Join(ErrInvalidInput, err)
	}
	if err == nil {
		a.markExtensionChange(botID, scope, "vault write")
	}
	return err
}

func vaultExtension(botID string, secrets vault.Snapshot) *durable.Extension {
	return &durable.Extension{
		Name: "vault",
		Tools: []*durable.ToolRegistration{
			JSONTool(botID, "secret_list", "List the available vault secrets by name, level, and last change. Values are not returned. Use vault:NAME to refer to a secret.",
				ObjectSchema([]string{}, map[string]any{}), true, func(context.Context, TaskContext, struct{}) ([]vault.Metadata, error) {
					return secrets.List(), nil
				}),
		},
		Hooks: []durable.HookRegistration{harness.Hook(harness.ToolTask, &harness.ToolHooks{
			AfterTool: func(_ context.Context, call harness.ToolHookCall, result durable.ToolExecutionResult, _ harness.HookApi) (*durable.ToolExecutionResult, error) {
				if call.Parent != nil || !secrets.HasMask() {
					return nil, nil
				}
				var output []ai.ToolResultMessageContent
				for i, block := range result.Output {
					text, ok := block.(ai.TextContent)
					if !ok {
						continue
					}
					masked := secrets.Mask(text.Text)
					if masked == text.Text {
						continue
					}
					if output == nil {
						output = slices.Clone(result.Output)
					}
					text.Text = masked
					output[i] = text
				}
				if output != nil {
					result.Output = output
				}
				result.Diagnostics = slices.Clone(result.Diagnostics)
				for i := range result.Diagnostics {
					result.Diagnostics[i].Message = secrets.Mask(result.Diagnostics[i].Message)
				}
				if result.HasDetails {
					result.Details = maskVaultJSON(result.Details, secrets)
				}
				return &result, nil
			},
		})},
	}
}

func maskVaultJSON(value any, secrets vault.Snapshot) any {
	switch value := value.(type) {
	case string:
		return secrets.Mask(value)
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[secrets.Mask(key)] = maskVaultJSON(item, secrets)
		}
		return out
	case durable.JsonObject:
		out := make(map[string]any, value.Len())
		for _, key := range value.Keys() {
			item, _ := value.Get(key)
			out[secrets.Mask(key)] = maskVaultJSON(item, secrets)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = maskVaultJSON(item, secrets)
		}
		return out
	default:
		return value
	}
}
