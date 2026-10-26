// Package agent owns agent settings, activity, and channel configuration.
package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/feishu"
	"github.com/recally-io/dex/internal/telegram"
	"github.com/recally-io/dex/internal/wechat"
)

// ErrInvalid means that a requested agent or channel change is invalid.
var ErrInvalid = errors.New("invalid settings")

// ErrNotFound means that the agent does not exist.
var ErrNotFound = errors.New("bot not found")

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Service owns agent writes and channel setup sessions.
type Service struct {
	q               *sqlc.Queries
	checkModel      func(context.Context, string, string) error
	wechatOptions   wechat.Options
	onWechat        func(string, *wechat.Config)
	wechatDB        *sql.DB
	ChannelStatus   func(string, string) channel.Status
	apiURL          string
	onTelegram      func(string, *telegram.Config)
	feishuAPIURL    string
	feishuRegistrar feishu.Registrar
	onFeishu        func(string, *feishu.Config)
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
	InboxUnread        int64 `json:"inboxUnread"`
	InboxFailed        int64 `json:"inboxFailed"`
	RoutineFailed      bool  `json:"routineFailed"`
	ActiveSessions     int64 `json:"activeSessions"`
	RunningChannels    int64 `json:"runningChannels"`
	EnabledAutomations int64 `json:"enabledAutomations"`
}

// Agent holds the settings of one agent. An empty ModelID means no model is chosen.
type Agent struct {
	Activity      *Activity `json:"activity,omitzero"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Provider      string    `json:"provider"`
	ModelID       string    `json:"modelId"`
	ThinkingLevel string    `json:"thinkingLevel"`
	Instructions  string    `json:"instructions"`
}

// Telegram describes the bot without its token.
type Telegram struct {
	Status       channel.Status `json:"status"`
	Enabled      bool           `json:"enabled"`
	BotID        string         `json:"botId,omitempty"`
	AllowedUsers []int64        `json:"allowedUsers"`
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
	return Agent{ID: row.ID, Name: row.Name, Provider: row.ModelProvider, ModelID: row.ModelID,
		ThinkingLevel: row.ThinkingLevel, Instructions: row.Instructions}
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
		current := Activity{InboxUnread: row.InboxUnread, InboxFailed: row.InboxFailed, RoutineFailed: row.RoutineFailed == 1, ActiveSessions: row.ActiveSessions, EnabledAutomations: row.EnabledAutomations}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Agent(ctx, id); err != nil {
		return err
	}
	a, err := s.cleanAgent(ctx, a)
	if err != nil {
		return err
	}
	return s.q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{
		ModelProvider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel, Instructions: a.Instructions,
		ID: id, Name: a.Name,
	})
}

// CreateAgent stores an agent without changing global provider configuration.
func (s *Service) CreateAgent(ctx context.Context, a Agent) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.cleanAgent(ctx, a)
	if err != nil {
		return Agent{}, err
	}
	row, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: uuid.NewV7().String(), Name: a.Name, ModelProvider: a.Provider, ModelID: a.ModelID,
		ThinkingLevel: a.ThinkingLevel, Instructions: a.Instructions, CreatedAt: time.Now().UnixMilli(),
	})
	return agentView(row), err
}

func (s *Service) cleanAgent(ctx context.Context, a Agent) (Agent, error) {
	a.Name = strings.TrimSpace(a.Name)
	if n := utf8.RuneCountInString(a.Name); n < 1 || n > 40 {
		return a, fmt.Errorf("%w: name must have 1 to 40 characters", ErrInvalid)
	}
	if !slices.Contains(thinkingLevels, a.ThinkingLevel) {
		return a, fmt.Errorf("%w: unknown thinking level %q", ErrInvalid, a.ThinkingLevel)
	}
	if a.ModelID != "" {
		if err := s.checkModel(ctx, a.Provider, a.ModelID); err != nil {
			return a, err
		}
	} else {
		a.Provider = ""
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
	return &telegram.Config{Token: row.Token, AllowedUsers: users, APIURL: s.apiURL, Pair: s.pairTelegram(agentID, row.Token)}, nil
}

// SetTelegram saves the bot configuration and restarts the bot. An empty token keeps the saved
// token.
func (s *Service) SetTelegram(ctx context.Context, agentID string, token string, allowedUsers []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		saved, err := s.TelegramConfig(ctx, agentID)
		if err != nil {
			return err
		}
		if saved != nil {
			token = saved.Token
		}
	}
	cfg := telegram.Config{Token: strings.TrimSpace(token), AllowedUsers: allowedUsers, APIURL: s.apiURL}
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
	err := s.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: agentID,
		Token: cfg.Token, AllowedUsers: strings.Join(users, ","),
	})
	if err != nil {
		return err
	}
	s.onTelegram(agentID, &cfg)
	return nil
}

// DeleteTelegram turns Telegram off and forgets the token.
func (s *Service) DeleteTelegram(ctx context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.q.DeleteTelegramSettings(ctx, agentID); err != nil {
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
