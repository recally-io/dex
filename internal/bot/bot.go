// Package bot owns bot settings and activity.
package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ErrInvalid means that a requested bot change is invalid.
var ErrInvalid = errors.New("invalid settings")

// ErrRunnerBusy is a change of the bot's environment while sessions that follow it have
// queued input or running background commands.
var ErrRunnerBusy = errors.New("sessions that use the environment of the bot have queued input or running background commands")

// ErrNotFound means that the bot does not exist.
var ErrNotFound = errors.New("bot not found")

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Service owns bot settings and activity.
type Service struct {
	q                   *sqlc.Queries
	checkModel          func(context.Context, string, string) error
	db                  *sql.DB
	RunningChannelCount func(string) int64
	OnRunnerChanged     func(ctx context.Context, botID, oldEnvID, oldSandboxID string)
	mu                  *sync.Mutex // shared with channel settings by app
}

// New connects storage, the shared channel lock, and model validation.
func New(q *sqlc.Queries, conn *sql.DB, mu *sync.Mutex, checkModel func(context.Context, string, string) error) *Service {
	return &Service{q: q, db: conn, mu: mu, checkModel: checkModel}
}

// Activity summarizes one bot for the bot list.
type Activity struct {
	AutomationFailed   bool  `json:"automationFailed"`
	ActiveSessions     int64 `json:"activeSessions"`
	RunningChannels    int64 `json:"runningChannels"`
	EnabledAutomations int64 `json:"enabledAutomations"`
}

// Bot holds the settings of one bot. An empty ModelID means no model is chosen.
type Bot struct {
	Activity         *Activity `json:"activity,omitzero"`
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Provider         string    `json:"provider"`
	ModelID          string    `json:"modelId"`
	ThinkingLevel    string    `json:"thinkingLevel"`
	FallbackProvider string    `json:"fallbackProvider"`
	FallbackModelID  string    `json:"fallbackModelId"`
	Instructions     string    `json:"instructions"`
	RunnerID         string    `json:"runnerId"`
	// ArchivedAt is the archive time in Unix milliseconds; zero for an active bot.
	ArchivedAt int64 `json:"archivedAt,omitzero"`
}

// Bot reads one bot.
func (s *Service) Bot(ctx context.Context, id string) (Bot, error) {
	row, err := s.q.GetBot(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Bot{}, ErrNotFound
	}
	return botView(row), err
}

func botView(row sqlc.Bot) Bot {
	return Bot{
		ID: row.ID, Name: row.Name, Provider: row.ModelProvider, ModelID: row.ModelID,
		ThinkingLevel: row.ThinkingLevel, FallbackProvider: row.FallbackProvider, FallbackModelID: row.FallbackModelID,
		Instructions: row.Instructions, RunnerID: row.RunnerID, ArchivedAt: row.ArchivedAt.Int64,
	}
}

// ListBots returns bots and their activity in creation order.
func (s *Service) ListBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.q.ListBotActivity(ctx)
	if err != nil {
		return nil, err
	}
	bots := make([]Bot, len(rows))
	for i, row := range rows {
		bots[i] = botView(row.Bot)
		current := Activity{AutomationFailed: row.AutomationFailed == 1, ActiveSessions: row.ActiveSessions, EnabledAutomations: row.EnabledAutomations}
		if s.RunningChannelCount != nil {
			current.RunningChannels = s.RunningChannelCount(row.Bot.ID)
		}
		bots[i].Activity = &current
	}
	return bots, nil
}

// SaveBot changes one bot. Its next message uses the settings.
func (s *Service) SaveBot(ctx context.Context, id string, a Bot) error {
	var oldRow sqlc.Bot
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		var getErr error
		oldRow, getErr = s.q.GetBot(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if getErr != nil {
			return getErr
		}
		var cleanErr error
		a, cleanErr = s.cleanBot(ctx, a)
		if cleanErr != nil {
			return cleanErr
		}
		if err := s.saveSettings(ctx, oldRow, sqlc.UpdateBotSettingsParams{
			ModelProvider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel,
			FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
			Instructions: a.Instructions, RunnerID: a.RunnerID, ID: id, Name: a.Name,
		}); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		return err
	}
	if oldRow.RunnerID != a.RunnerID && s.OnRunnerChanged != nil {
		s.OnRunnerChanged(ctx, id, oldRow.RunnerID, oldRow.RunnerSandboxID)
	}
	return nil
}

// CreateBot stores a bot. Fields the patch omits come from the default bot.
func (s *Service) CreateBot(ctx context.Context, p Patch) (Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.withDefaults(ctx, p)
	if err != nil {
		return Bot{}, err
	}
	a := Bot{ThinkingLevel: "off"}
	applyPatch(&a, p)
	if a, err = s.cleanBot(ctx, a); err != nil {
		return Bot{}, err
	}
	row, err := s.q.CreateBot(ctx, sqlc.CreateBotParams{
		ID: uuid.NewV7().String(), Name: a.Name, ModelProvider: a.Provider, ModelID: a.ModelID,
		ThinkingLevel: a.ThinkingLevel, FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
		Instructions: a.Instructions, RunnerID: a.RunnerID, CreatedAt: time.Now().UnixMilli(),
	})
	return botView(row), err
}

// withDefaults fills the fields that p omits from the built-in default bot, so a new bot starts
// with its model, thinking level, fallback model, and execution environment. A saved model that
// is no longer available is not copied.
func (s *Service) withDefaults(ctx context.Context, p Patch) (Patch, error) {
	row, err := s.q.GetBot(ctx, "default")
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return Patch{}, err
	}
	d := botView(row)
	usable := func(provider, id string) (bool, error) {
		if id == "" {
			return false, nil
		}
		err := s.checkModel(ctx, provider, id)
		if errors.Is(err, engine.ErrModelsPending) {
			return false, err
		}
		return err == nil, nil
	}
	if p.Provider == nil && p.ModelID == nil {
		ok, err := usable(d.Provider, d.ModelID)
		if err != nil {
			return Patch{}, err
		}
		if ok {
			p.Provider, p.ModelID = &d.Provider, &d.ModelID
		}
	}
	if p.FallbackProvider == nil && p.FallbackModelID == nil && p.ModelID != nil && *p.ModelID != "" {
		ok, err := usable(d.FallbackProvider, d.FallbackModelID)
		if err != nil {
			return Patch{}, err
		}
		if ok {
			p.FallbackProvider, p.FallbackModelID = &d.FallbackProvider, &d.FallbackModelID
		}
	}
	if p.ThinkingLevel == nil {
		p.ThinkingLevel = &d.ThinkingLevel
	}
	if p.RunnerID == nil {
		p.RunnerID = &d.RunnerID
	}
	return p, nil
}

func (s *Service) cleanBot(ctx context.Context, a Bot) (Bot, error) {
	a.Name = strings.TrimSpace(a.Name)
	if n := utf8.RuneCountInString(a.Name); n < 1 || n > 40 {
		return a, fmt.Errorf("%w: name must have 1 to 40 characters", ErrInvalid)
	}
	if !slices.Contains(thinkingLevels, a.ThinkingLevel) {
		return a, fmt.Errorf("%w: unknown thinking level %q", ErrInvalid, a.ThinkingLevel)
	}
	a.RunnerID = strings.TrimSpace(a.RunnerID)
	if a.RunnerID == "local" {
		a.RunnerID = ""
	}
	if a.RunnerID != "" {
		if _, err := s.q.GetRunner(ctx, a.RunnerID); errors.Is(err, sql.ErrNoRows) {
			return a, fmt.Errorf("%w: execution environment %q does not exist", ErrInvalid, a.RunnerID)
		} else if err != nil {
			return a, err
		}
	}
	a.Provider = strings.TrimSpace(a.Provider)
	a.ModelID = strings.TrimSpace(a.ModelID)
	if a.ModelID != "" {
		if err := s.checkModel(ctx, a.Provider, a.ModelID); err != nil {
			return a, err
		}
	} else {
		a.Provider = ""
	}
	a.FallbackProvider = strings.TrimSpace(a.FallbackProvider)
	a.FallbackModelID = strings.TrimSpace(a.FallbackModelID)
	if a.FallbackModelID != "" {
		if err := s.checkModel(ctx, a.FallbackProvider, a.FallbackModelID); err != nil {
			return a, err
		}
	} else {
		a.FallbackProvider = ""
	}
	if a.FallbackModelID != "" && a.ModelID == "" {
		return a, fmt.Errorf("%w: choose a model before a fallback model", ErrInvalid)
	}
	return a, nil
}

// Home returns the storage root for a bot in the resolved data directory.
func Home(dataDir, id string) string {
	return filepath.Join(dataDir, "agents", id)
}
