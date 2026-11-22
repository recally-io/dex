// Package channel defines the shared channel transport contract and frozen delivery values.
// Peers have the form "platform:remote-id" and contain no slash.
// Source keys identify inbound updates globally and include the bot identity.
package channel

import (
	"context"
	"errors"
	"strings"
	"unicode"
)

// ErrPermanent marks a part the platform cannot accept. The sender logs and skips it.
// Other errors keep the part for retry.
var ErrPermanent = errors.New("permanent channel delivery failure")

// ErrGroupRejected permits a durable switch to single-file delivery after a definitive rejection.
var ErrGroupRejected = errors.New("media group was rejected")

// Status is the public runtime state. Reason must contain no credentials.
type Status struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Bot is one platform transport. The application creates concrete bots and
// registers them with Manager.Set; this package never imports an adapter.
//
// Run and Send can execute at the same time, and Send can run while Run is
// receiving. When ctx is cancelled, each method must stop and join the
// goroutines, connections, and timers that it started before it returns.
type Bot interface {
	// Run receives platform input until ctx ends or the transport fails.
	Run(context.Context) error
	// Parts renders and splits a final reply for this platform. Each Text is
	// already in the platform's own format. Manager stores a reply's parts
	// before its first Send, so a retry sends the same parts.
	Parts(Reply) []Part
	// Send delivers one part. Replies use the submission ID; commands use the
	// source key. Part is zero-based. The receipt contains every remote
	// message ID for that part. ErrPermanent rejects the part; other errors keep
	// it for retry. A Part variant that this adapter cannot deliver is the
	// adapter's own decision; it does not change what other platforms support.
	Send(ctx context.Context, peer, deliveryID string, part int, content Part) (Receipt, error)
}

// Reply is the settled answer or the reason no answer is available.
type Reply struct {
	Status string // "done" or "unanswered"
	Answer string
	Reason string
}

const FeedbackCommand = "/feedback"

// SplitCommand separates a command token from the text after it, including on multiline input.
func SplitCommand(text string) (name, rest string) {
	text = strings.TrimSpace(text)
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		return text[:i], strings.TrimLeftFunc(text[i:], unicode.IsSpace)
	}
	return text, ""
}

// IsFeedbackCommand matches the full token, not a prefix of an ordinary message.
func IsFeedbackCommand(text string) bool {
	name, _ := SplitCommand(text)
	return name == FeedbackCommand
}

// IsCommand reports whether trimmed channel text is a command rather than input for the model.
func IsCommand(text string) bool {
	switch text {
	case "/clear", "/new", "/stop", "/help", "/menu", "/status", "/settings", "/sessions", "/workflows", "/question":
		return true
	}
	return IsFeedbackCommand(text) || strings.HasPrefix(text, "/run ") || strings.HasPrefix(text, "/answer ")
}
