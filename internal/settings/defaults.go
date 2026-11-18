package settings

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// BotDefaults are the global default settings applied when creating a new bot.
type BotDefaults struct {
	Provider         string `json:"provider"`
	ModelID          string `json:"modelId"`
	ThinkingLevel    string `json:"thinkingLevel"`
	FallbackProvider string `json:"fallbackProvider"`
	FallbackModelID  string `json:"fallbackModelId"`
	EnvironmentID    string `json:"environmentId"`
}

// Defaults returns the saved bot defaults, or the zero-value defaults ("off" thinking) when unset.
func (s *Service) Defaults(ctx context.Context) (BotDefaults, error) {
	row, err := s.q.GetBotDefaults(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return BotDefaults{ThinkingLevel: "off"}, nil
	}
	if err != nil {
		return BotDefaults{}, err
	}
	return BotDefaults{
		Provider:         row.ModelProvider,
		ModelID:          row.ModelID,
		ThinkingLevel:    cmp.Or(row.ThinkingLevel, "off"),
		FallbackProvider: row.FallbackProvider,
		FallbackModelID:  row.FallbackModelID,
		EnvironmentID:    row.EnvironmentID,
	}, nil
}

// SetDefaults validates and saves global defaults for new bots.
// Never acquires the agent service lock; callers handle default-bot backfill separately.
func (s *Service) SetDefaults(ctx context.Context, d BotDefaults) (BotDefaults, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d.ThinkingLevel = cmp.Or(strings.TrimSpace(d.ThinkingLevel), "off")
	if !slices.Contains(thinkingLevels, d.ThinkingLevel) {
		return BotDefaults{}, fmt.Errorf("%w: unknown thinking level %q", ErrInvalid, d.ThinkingLevel)
	}
	d.Provider, d.ModelID = strings.TrimSpace(d.Provider), strings.TrimSpace(d.ModelID)
	if (d.Provider == "") != (d.ModelID == "") {
		return BotDefaults{}, fmt.Errorf("%w: give both the provider and the model ID, or neither", ErrInvalid)
	}
	if d.ModelID != "" {
		if err := s.checkModel(ctx, d.Provider, d.ModelID); err != nil {
			return BotDefaults{}, err
		}
	}
	d.FallbackProvider, d.FallbackModelID = strings.TrimSpace(d.FallbackProvider), strings.TrimSpace(d.FallbackModelID)
	if (d.FallbackProvider == "") != (d.FallbackModelID == "") {
		return BotDefaults{}, fmt.Errorf("%w: give both the fallback provider and the fallback model ID, or neither", ErrInvalid)
	}
	if d.FallbackModelID != "" {
		if err := s.checkModel(ctx, d.FallbackProvider, d.FallbackModelID); err != nil {
			return BotDefaults{}, err
		}
	}
	if d.FallbackModelID != "" && d.ModelID == "" {
		return BotDefaults{}, fmt.Errorf("%w: choose a default model before a fallback model", ErrInvalid)
	}
	d.EnvironmentID = strings.TrimSpace(d.EnvironmentID)
	if d.EnvironmentID == "local" {
		d.EnvironmentID = ""
	}
	if d.EnvironmentID != "" {
		if _, err := s.q.GetEnvironment(ctx, d.EnvironmentID); errors.Is(err, sql.ErrNoRows) {
			return BotDefaults{}, fmt.Errorf("%w: execution environment %q does not exist", ErrInvalid, d.EnvironmentID)
		} else if err != nil {
			return BotDefaults{}, err
		}
	}

	err := s.q.SetBotDefaults(ctx, sqlc.SetBotDefaultsParams{
		ModelProvider:    d.Provider,
		ModelID:          d.ModelID,
		ThinkingLevel:    d.ThinkingLevel,
		FallbackProvider: d.FallbackProvider,
		FallbackModelID:  d.FallbackModelID,
		EnvironmentID:    d.EnvironmentID,
	})
	if err != nil {
		return BotDefaults{}, err
	}
	return d, nil
}
