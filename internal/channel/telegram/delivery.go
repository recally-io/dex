package telegram

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/feedback"
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
	if reply.Literal {
		// Only the fixed, app-supplied link becomes markup. The copyable block is literal.
		header, draft, hasDraft := strings.Cut(text, "\n"+feedback.IssueURL+"\n\n")
		rich := "<p>" + html.EscapeString(header) + "</p>"
		if hasDraft {
			rich += `<p><a href="` + feedback.IssueURL + `">Open GitHub new issue</a></p><pre><code>` + html.EscapeString(draft) + "</code></pre>"
		}
		return []channel.Part{{Rich: &channel.RichMessage{Version: 1, HTML: rich}}}
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
