package delivery

import (
	"strings"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/channel"
)

const (
	feedbackRepository    = "recally-io/dex"
	feedbackIssueURL      = "https://github.com/" + feedbackRepository + "/issues/new"
	feedbackPublicWarning = feedbackRepository + " is public. Remove secrets and private details before posting."
	feedbackInstructions  = "Send /feedback followed by your title on the first line and your description on the lines below. " + feedbackPublicWarning
	feedbackTooLong       = "This draft is too long for one channel reply. Shorten it or use Feedback in Web."
)

// feedbackReply builds a preview from explicitly supplied text. The user submits it.
func feedbackReply(text string) string {
	_, rest := channel.SplitCommand(text)
	if rest == "" {
		return feedbackInstructions
	}
	title, description, _ := strings.Cut(rest, "\n")
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	if title == "" || strings.ContainsAny(title, "\r\n") || utf8.RuneCountInString(title) > 120 {
		return "Draft not ready: enter a title on one line (up to 120 characters). " + feedbackInstructions
	}
	if description == "" || utf8.RuneCountInString(description) > 1500 {
		return "Draft not ready: enter a description (up to 1500 characters). " + feedbackInstructions
	}
	const fence = "```"
	if strings.Contains(title, fence) || strings.Contains(description, fence) {
		return "This draft contains code fences. Remove them or use Feedback in Web."
	}
	answer := feedbackPublicWarning + " Copy the title and description below. Submit with your own GitHub account:\n\n" + feedbackIssueURL + "\n\n" + fence + "\nTitle: " + title + "\n\nDescription:\n" + description + "\n" + fence
	// Keep the complete draft in one message.
	if len(answer) > 4000 {
		return feedbackTooLong
	}
	return answer
}
