package telegram

import (
	"context"
	"errors"
	"regexp"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

// Username gets the public bot name from Telegram. It never returns the token.
func Username(ctx context.Context, cfg Config) (string, error) {
	bot := New(cfg, nil)
	var me struct {
		Username string `json:"username"`
	}
	if err := bot.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return "", err
	}
	if !usernamePattern.MatchString(me.Username) {
		return "", errors.New("telegram returned an invalid bot username")
	}
	return me.Username, nil
}
