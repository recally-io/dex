package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
)

// Parts freezes all text as Rich Messages.
func (b *Bot) Parts(reply chat.Reply) []channel.Part {
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer: " + reply.Reason
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	return renderRich(text)
}

func parsePeer(key string) (int64, int64, error) {
	remote, ok := strings.CutPrefix(key, channelPrefix)
	chat, topic, thread := strings.Cut(remote, ":")
	chatID, err := strconv.ParseInt(chat, 10, 64)
	if !ok || err != nil || chatID == 0 || strconv.FormatInt(chatID, 10) != chat {
		return 0, 0, fmt.Errorf("%w: invalid Telegram destination", channel.ErrPermanent)
	}
	var topicID int64
	if thread {
		topicID, err = strconv.ParseInt(topic, 10, 64)
		if chatID > 0 || err != nil || topicID <= 0 || strconv.FormatInt(topicID, 10) != topic {
			return 0, 0, fmt.Errorf("%w: invalid Telegram topic", channel.ErrPermanent)
		}
	}
	return chatID, topicID, nil
}

func peerID(key string) (int64, error) { chatID, _, err := parsePeer(key); return chatID, err }
