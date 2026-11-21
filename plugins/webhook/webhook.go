// Package webhook is the generic Webhook plugin. It converts signed deliveries of a source into Task events.
// A source can be any service that signs a normalized JSON event, for example a calendar bridge.
package webhook

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/worktask"
)

// MaxPayload is the maximum size of one delivery.
const MaxPayload = 2 << 20

var (
	// ErrSignature means the delivery does not have a valid SHA-256 signature.
	ErrSignature = errors.New("invalid webhook signature")
	// ErrPayload means the delivery is not a valid normalized event.
	ErrPayload = errors.New("invalid webhook payload")
)

// AuthorClass marks events from a signed generic source. The source has no per-author identity.
const AuthorClass = "SOURCE"

// Rule is the source rule for events that match no Task.
type Rule struct {
	// TaskAgentID creates a Task under this agent for an unmatched event with exactly one object.
	TaskAgentID string
	// HoldUnmatched keeps other unmatched events for the owner.
	HoldUnmatched bool
}

// Namespace comes from the configured source ID, never from the payload,
// so a source cannot write into the objects of another source.
func Namespace(sourceID string) string { return "webhook/" + sourceID }

type payload struct {
	Type       string            `json:"type"`
	ChangeKey  string            `json:"changeKey"`
	Objects    []worktask.Object `json:"objects"`
	Revision   string            `json:"revision"`
	Title      string            `json:"title"`
	Content    string            `json:"content"`
	SourceURL  string            `json:"sourceUrl"`
	OccurredAt time.Time         `json:"occurredAt"`
}

// Accept verifies a delivery and commits its event before it returns the receipt.
// The delivery ID is the receipt key, so a retry of the same delivery is a duplicate.
func Accept(ctx context.Context, store *worktask.Service, sourceID string, secret []byte, delivery, signature string, body []byte, rule Rule) (worktask.Receipt, error) {
	if len(body) > MaxPayload || delivery == "" {
		return worktask.Receipt{}, ErrPayload
	}
	if !validSignature(secret, signature, body) {
		return worktask.Receipt{}, ErrSignature
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return worktask.Receipt{}, errors.Join(ErrPayload, err)
	}
	title := strings.TrimSpace(p.Title)
	if len(p.Objects) == 0 || utf8.RuneCountInString(title) > 160 || p.SourceURL != "" && !strings.HasPrefix(p.SourceURL, "https://") {
		return worktask.Receipt{}, ErrPayload
	}
	event := worktask.Event{
		Namespace: Namespace(sourceID), ReceiptKey: "delivery:" + delivery, ChangeKey: p.ChangeKey, Type: p.Type,
		Objects: p.Objects, Revision: p.Revision, Content: p.Content, SourceURL: p.SourceURL, OccurredAt: p.OccurredAt.UnixMilli(),
		AuthorAssociation: AuthorClass, HoldUnmatched: rule.HoldUnmatched,
	}
	if rule.TaskAgentID == "" || len(p.Objects) != 1 {
		return store.Accept(ctx, event)
	}
	return store.AcceptCreating(ctx, event, rule.TaskAgentID, cmp.Or(title, p.Type))
}

func validSignature(secret []byte, signature string, body []byte) bool {
	encoded, ok := strings.CutPrefix(signature, "sha256=")
	if !ok || len(secret) == 0 || len(encoded) != 2*sha256.Size {
		return false
	}
	digest, err := hex.DecodeString(encoded)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(digest, mac.Sum(nil))
}
