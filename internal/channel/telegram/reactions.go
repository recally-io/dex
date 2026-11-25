package telegram

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/channel"
)

func (b *Bot) ReactionsEnabled(peer string) bool {
	if strings.HasPrefix(peer, "telegram:-") {
		return b.cfg.Options.GroupReactions
	}
	return b.cfg.Options.Reactions == nil || *b.cfg.Options.Reactions
}

func (b *Bot) React(ctx context.Context, peer, messageID, _ string, status channel.Reaction) (string, error) {
	emoji := map[channel.Reaction]string{
		channel.ReactionQueued: "👀", channel.ReactionProcessing: "🔥", channel.ReactionSucceeded: "👍",
		channel.ReactionFailed: "👎", channel.ReactionCancelled: "💔",
	}[status]
	chatID, err := peerID(peer)
	if err != nil {
		return "", err
	}
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || id <= 0 || emoji == "" {
		return "", errors.New("invalid telegram status reaction")
	}
	payload, err := json.Marshal(map[string]any{"chat_id": chatID, "message_id": id, "reaction": []map[string]string{{"type": "emoji", "emoji": emoji}}})
	if err != nil {
		return "", err
	}
	var done bool
	// Status reactions must not share or persist the reply/polling FloodWait window.
	err = b.requestAPI(ctx, "setMessageReaction", bytes.NewReader(payload), "application/json", &done, false)
	if api, ok := errors.AsType[*apiError](err); ok && api.Code >= 400 && api.Code < 500 {
		if api.Code == 429 {
			seconds := min(int64(api.RetryAfter), math.MaxInt64/int64(time.Second))
			return "", &channel.RetryError{After: time.Duration(seconds) * time.Second, Err: err}
		}
		return "", err
	}
	if err != nil || !done {
		return "", channel.ErrReactionUnknown
	}
	return "", nil
}
