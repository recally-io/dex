package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/channel"
)

// Parts freezes all text as Rich Messages.
func (b *Bot) Parts(reply channel.Reply) []channel.Part {
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer: " + reply.Reason
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	return renderRich(text)
}

// peerID accepts only positive private Telegram destinations.
func peerID(key string) (int64, error) {
	remote, ok := strings.CutPrefix(key, channelPrefix)
	id, err := strconv.ParseInt(remote, 10, 64)
	if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != remote {
		return 0, fmt.Errorf("%w: invalid private Telegram peer", channel.ErrPermanent)
	}
	return id, nil
}
