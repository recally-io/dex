// Package agent owns agent settings, activity, and channel configuration.
package agent

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/wechat"
)

// ErrInvalid means that a requested agent or channel change is invalid.
var ErrInvalid = errors.New("invalid settings")

// ErrNotFound means that the agent does not exist.
var ErrNotFound = errors.New("bot not found")

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Service owns agent writes and channel setup sessions.
type Service struct {
	q                    *sqlc.Queries
	checkModel           func(context.Context, string, string) error
	wechatOptions        wechat.Options
	onWechat             func(string, *wechat.Config)
	wechatDB             *sql.DB
	ChannelStatus        func(string, string) channel.Status
	OnEnvironmentChanged func(ctx context.Context, agentID, oldEnvID, oldSandboxID string)
	apiURL               string
	onTelegram           func(string, *telegram.Config)
	feishuAPIURL         string
	feishuRegistrar      feishu.Registrar
	onFeishu             func(string, *feishu.Config)
	// One lock orders credential checks, saves, runtime changes, and setup sessions
	// across agents. Model checks take the settings lock; settings never takes this lock.
	mu       sync.Mutex
	sessions map[string]*channelSession
}

// New connects model validation and Telegram runtime changes.
func New(q *sqlc.Queries, checkModel func(context.Context, string, string) error, telegramAPIURL string, onTelegram func(string, *telegram.Config)) *Service {
	return &Service{q: q, checkModel: checkModel, apiURL: telegramAPIURL, onTelegram: onTelegram}
}

// Activity summarizes one agent for the agent list.
type Activity struct {
	RoutineFailed      bool  `json:"routineFailed"`
	ActiveSessions     int64 `json:"activeSessions"`
	RunningChannels    int64 `json:"runningChannels"`
	EnabledAutomations int64 `json:"enabledAutomations"`
}

// Agent holds the settings of one agent. An empty ModelID means no model is chosen.
type Agent struct {
	Activity         *Activity `json:"activity,omitzero"`
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Provider         string    `json:"provider"`
	ModelID          string    `json:"modelId"`
	ThinkingLevel    string    `json:"thinkingLevel"`
	FallbackProvider string    `json:"fallbackProvider"`
	FallbackModelID  string    `json:"fallbackModelId"`
	Instructions     string    `json:"instructions"`
	EnvironmentID    string    `json:"environmentId"`
	// ArchivedAt is the archive time in Unix milliseconds; zero for an active bot.
	ArchivedAt int64 `json:"archivedAt,omitzero"`
}

// Telegram describes the bot without its token.
type Telegram struct {
	Status       channel.Status   `json:"status"`
	Enabled      bool             `json:"enabled"`
	BotID        string           `json:"botId,omitempty"`
	AllowedUsers []int64          `json:"allowedUsers"`
	Options      telegram.Options `json:"options"`
}

// Agent reads one agent.
func (s *Service) Agent(ctx context.Context, id string) (Agent, error) {
	row, err := s.q.GetAgent(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return agentView(row), err
}

func agentView(row sqlc.Agent) Agent {
	return Agent{
		ID: row.ID, Name: row.Name, Provider: row.ModelProvider, ModelID: row.ModelID,
		ThinkingLevel: row.ThinkingLevel, FallbackProvider: row.FallbackProvider, FallbackModelID: row.FallbackModelID,
		Instructions: row.Instructions, EnvironmentID: row.EnvironmentID, ArchivedAt: row.ArchivedAt.Int64,
	}
}

// ListAgents returns agents and their activity in creation order.
func (s *Service) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.ListAgentActivity(ctx)
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, len(rows))
	for i, row := range rows {
		agents[i] = agentView(row.Agent)
		current := Activity{RoutineFailed: row.RoutineFailed == 1, ActiveSessions: row.ActiveSessions, EnabledAutomations: row.EnabledAutomations}
		if s.ChannelStatus != nil {
			for _, platform := range []string{"telegram", "wechat", "feishu"} {
				if s.ChannelStatus(row.Agent.ID, platform).State == "running" {
					current.RunningChannels++
				}
			}
		}
		agents[i].Activity = &current
	}
	return agents, nil
}

// SaveAgent changes one agent. Its next message uses the settings.
func (s *Service) SaveAgent(ctx context.Context, id string, a Agent) error {
	var oldRow sqlc.Agent
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		var getErr error
		oldRow, getErr = s.q.GetAgent(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if getErr != nil {
			return getErr
		}
		var cleanErr error
		a, cleanErr = s.cleanAgent(ctx, a)
		if cleanErr != nil {
			return cleanErr
		}
		return s.q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{
			ModelProvider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel,
			FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
			Instructions: a.Instructions, EnvironmentID: a.EnvironmentID, ID: id, Name: a.Name,
		})
	}()
	if err != nil {
		return err
	}
	if oldRow.EnvironmentID != a.EnvironmentID && s.OnEnvironmentChanged != nil {
		s.OnEnvironmentChanged(ctx, id, oldRow.EnvironmentID, oldRow.EnvironmentSandboxID)
	}
	return nil
}

// CreateAgent stores an agent. Fields the patch omits come from the default bot.
func (s *Service) CreateAgent(ctx context.Context, p Patch) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.withDefaults(ctx, p)
	if err != nil {
		return Agent{}, err
	}
	a := Agent{ThinkingLevel: "off"}
	applyPatch(&a, p)
	if a, err = s.cleanAgent(ctx, a); err != nil {
		return Agent{}, err
	}
	row, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: uuid.NewV7().String(), Name: a.Name, ModelProvider: a.Provider, ModelID: a.ModelID,
		ThinkingLevel: a.ThinkingLevel, FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
		Instructions: a.Instructions, EnvironmentID: a.EnvironmentID, CreatedAt: time.Now().UnixMilli(),
	})
	return agentView(row), err
}

// withDefaults fills the fields that p omits from the built-in default bot, so a new bot starts
// with its model, thinking level, fallback model, and execution environment. A saved model that
// is no longer available is not copied.
func (s *Service) withDefaults(ctx context.Context, p Patch) (Patch, error) {
	row, err := s.q.GetAgent(ctx, "default")
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return Patch{}, err
	}
	d := agentView(row)
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
	if p.EnvironmentID == nil {
		p.EnvironmentID = &d.EnvironmentID
	}
	return p, nil
}

func (s *Service) cleanAgent(ctx context.Context, a Agent) (Agent, error) {
	a.Name = strings.TrimSpace(a.Name)
	if n := utf8.RuneCountInString(a.Name); n < 1 || n > 40 {
		return a, fmt.Errorf("%w: name must have 1 to 40 characters", ErrInvalid)
	}
	if !slices.Contains(thinkingLevels, a.ThinkingLevel) {
		return a, fmt.Errorf("%w: unknown thinking level %q", ErrInvalid, a.ThinkingLevel)
	}
	a.EnvironmentID = strings.TrimSpace(a.EnvironmentID)
	if a.EnvironmentID == "local" {
		a.EnvironmentID = ""
	}
	if a.EnvironmentID != "" {
		if _, err := s.q.GetEnvironment(ctx, a.EnvironmentID); errors.Is(err, sql.ErrNoRows) {
			return a, fmt.Errorf("%w: execution environment %q does not exist", ErrInvalid, a.EnvironmentID)
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

// TelegramConfig returns the saved bot configuration, or nil when Telegram is off.
func (s *Service) TelegramConfig(ctx context.Context, agentID string) (*telegram.Config, error) {
	row, err := s.q.GetTelegramSettings(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	users, err := parseUsers(row.AllowedUsers)
	if err != nil {
		return nil, err
	}
	var options telegram.Options
	if err := json.Unmarshal([]byte(row.Options), &options); err != nil {
		return nil, err
	}
	return &telegram.Config{Options: options, Token: row.Token, AllowedUsers: users, APIURL: s.apiURL, Pair: s.pairTelegram(agentID, row.Token), RetryAt: row.RetryAt}, nil
}

// SetTelegram saves the bot configuration and restarts the bot. An empty token keeps the saved
// token.
func (s *Service) SetTelegram(ctx context.Context, agentID string, token string, allowedUsers []int64, options *telegram.Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := db.RequireActive(ctx, s.q, agentID); err != nil {
		return err
	}
	// Pairing uses TryLock, so this snapshot cannot change before the old bot joins.
	saved, err := s.TelegramConfig(ctx, agentID)
	if err != nil {
		return err
	}
	s.onTelegram(agentID, nil)
	committed := false
	defer func() {
		if !committed && saved != nil {
			s.onTelegram(agentID, saved)
		}
	}()
	if token == "" {
		if saved != nil {
			token = saved.Token
		}
	}
	cfg := telegram.Config{Token: strings.TrimSpace(token), AllowedUsers: allowedUsers, APIURL: s.apiURL}
	if saved != nil {
		cfg.Options = saved.Options
	}
	if options != nil {
		cfg.Options = *options
	}
	cfg.Pair = s.pairTelegram(agentID, cfg.Token)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := s.checkCredential(ctx, agentID, "telegram", cfg.Token); err != nil {
		return err
	}
	users := make([]string, len(allowedUsers))
	for i, id := range allowedUsers {
		users[i] = strconv.FormatInt(id, 10)
	}
	encoded, err := json.Marshal(cfg.Options)
	if err != nil {
		return err
	}
	cfg.RetryAt, err = s.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: agentID,
		Token: cfg.Token, AllowedUsers: strings.Join(users, ","), Options: string(encoded),
	})
	if err != nil {
		return err
	}
	committed = true
	s.onTelegram(agentID, &cfg)
	return nil
}

// DeleteTelegram turns Telegram off and forgets the token.
func (s *Service) DeleteTelegram(ctx context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, err := s.TelegramConfig(ctx, agentID)
	if err != nil {
		return err
	}
	s.onTelegram(agentID, nil)
	if err := s.q.DeleteTelegramSettings(ctx, agentID); err != nil {
		if saved != nil {
			s.onTelegram(agentID, saved)
		}
		return err
	}
	s.onTelegram(agentID, nil)
	return nil
}

func parseUsers(s string) ([]int64, error) {
	users := []int64{}
	if s == "" {
		return users, nil
	}
	for field := range strings.SplitSeq(s, ",") {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("telegram_settings.allowed_users: %w", err)
		}
		users = append(users, id)
	}
	return users, nil
}

// Home returns the storage root for an agent in the resolved data directory.
func Home(dataDir, id string) string {
	return filepath.Join(dataDir, "agents", id)
}
