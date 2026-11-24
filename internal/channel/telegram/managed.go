package telegram

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var botUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

// ManagedUpdate is the managed_bot payload of a Bot API update.
type ManagedUpdate struct {
	User user `json:"user"`
	Bot  user `json:"bot"`
}

// Manager uses a dedicated Bot API account for setup. It never handles agent messages.
type Manager struct{ bot *Bot }

func NewManager(apiURL, token string) *Manager {
	b := New(Config{APIURL: apiURL, Token: token}, nil)
	b.client.Timeout = 25 * time.Second
	b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Manager{bot: b}
}

type botManagementInfo struct {
	ID            int64  `json:"id"`
	Username      string `json:"username"`
	IsBot         bool   `json:"is_bot"`
	CanManageBots bool   `json:"can_manage_bots"`
}

func (m *Manager) botIdentity(ctx context.Context, id int64) (botManagementInfo, error) {
	var info botManagementInfo
	if err := m.bot.call(ctx, "getMe", nil, &info); err != nil || !info.IsBot || id <= 0 || info.ID != id {
		return botManagementInfo{}, errors.New("could not validate the bot token with Telegram")
	}
	return info, nil
}

// ValidateManagedToken checks that the credential works for the claimed bot.
func ValidateManagedToken(ctx context.Context, apiURL, token string, id int64) error {
	_, err := NewManager(apiURL, token).botIdentity(ctx, id)
	return err
}

// ManagementIdentity checks BotFather's management setting through getMe.
func (m *Manager) ManagementIdentity(ctx context.Context, id int64) (string, error) {
	info, err := m.botIdentity(ctx, id)
	if err != nil {
		return "", err
	}
	if !info.CanManageBots || !botUsernamePattern.MatchString(info.Username) {
		return "", errors.New("enable Bot Management Mode for the manager bot in BotFather")
	}
	return info.Username, nil
}

// Poll processes manager updates in order. A failed handler leaves its update unconfirmed.
func (m *Manager) Poll(ctx context.Context, offset int64, pair func(string, int64) error, managed func(ManagedUpdate) error) (int64, error) {
	var updates []struct {
		ID      int64          `json:"update_id"`
		Message *message       `json:"message"`
		Managed *ManagedUpdate `json:"managed_bot"`
	}
	if err := m.bot.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": 15, "allowed_updates": []string{"message", "managed_bot"},
	}, &updates); err != nil {
		return offset, errors.New("could not read Telegram manager updates")
	}
	for _, update := range updates {
		msg := update.Message
		switch {
		case update.Managed != nil:
			if err := managed(*update.Managed); err != nil {
				return offset, err
			}
		case msg != nil && msg.Chat.Type == "private" && msg.From != nil && !msg.From.IsBot && msg.From.ID > 0 && msg.ForwardOrigin == nil:
			if id, ok := strings.CutPrefix(msg.Text, "/start "); ok {
				if err := pair(id, msg.From.ID); err != nil {
					return offset, err
				}
			}
		}
		offset = max(offset, update.ID+1)
	}
	return offset, nil
}

// ManagedToken fetches a credential by the managed bot's user ID. Do not return Bot API error text:
// a malformed response or remote description can include the credential.
func (m *Manager) ManagedToken(ctx context.Context, id int64) (string, error) {
	var token string
	if err := m.bot.call(ctx, "getManagedBotToken", map[string]int64{"user_id": id}, &token); err != nil {
		return "", errors.New("could not read the managed bot token from Telegram")
	}
	return token, nil
}
