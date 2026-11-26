// Package email is the email plugin. A mail forwarding service posts raw RFC 5322 messages
// of a selected address, and the thread headers route each message to its Task.
package email

import (
	"cmp"
	"context"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/plugin"
	tasksvc "github.com/recally-io/dex/internal/task"
)

// MaxPayload is the maximum size of one message. The forwarder removes large files first.
const MaxPayload = 2 << 20

var (
	// ErrSignature means the delivery does not have a valid SHA-256 signature.
	ErrSignature = errors.New("invalid email signature")
	// ErrPayload means the delivery is not a valid message.
	ErrPayload = errors.New("invalid email message")
)

// AuthorClass marks events from a signed source. Anyone can forge a From header, so it is not an author.
const AuthorClass = "SOURCE"

// Source is one configured address.
type Source struct {
	ID     string
	Secret []byte
	// TaskBotID creates a Task under this bot for a new thread.
	TaskBotID string
	// HoldUnmatched keeps other unmatched messages for the owner.
	HoldUnmatched bool
}

// Namespace comes from the configured source ID, never from the message,
// so a source cannot write into the objects of another source.
func Namespace(sourceID string) string { return "email/" + sourceID }

type payload struct {
	Type       string
	ChangeKey  string
	Objects    []tasksvc.Object
	Title      string
	Content    string
	OccurredAt time.Time
}

// Accept verifies a delivery and commits its event before it returns the receipt.
// The delivery ID is the receipt key, and the Message-ID is the change key, so a retry adds nothing.
func Accept(ctx context.Context, store *tasksvc.Service, source Source, delivery, signature string, body []byte) (tasksvc.Receipt, error) {
	if len(body) > MaxPayload || delivery == "" {
		return tasksvc.Receipt{}, ErrPayload
	}
	if !plugin.ValidSignature(source.Secret, signature, body) {
		return tasksvc.Receipt{}, ErrSignature
	}
	return accept(ctx, store, source, "delivery:"+delivery, body)
}

// accept stores one message from the hook or from IMAP Pull. Both use the namespace of the source,
// so a forwarded and a pulled copy of one message share the change key and add one entry.
func accept(ctx context.Context, store *tasksvc.Service, source Source, receiptKey string, body []byte) (tasksvc.Receipt, error) {
	p, err := parseEmail(body, time.Now())
	if err != nil {
		return tasksvc.Receipt{}, errors.Join(ErrPayload, err)
	}
	event := tasksvc.Event{
		Namespace: Namespace(source.ID), ReceiptKey: receiptKey, ChangeKey: p.ChangeKey, Type: p.Type,
		Objects: p.Objects, Content: p.Content, OccurredAt: p.OccurredAt.UnixMilli(),
		AuthorAssociation: AuthorClass, HoldUnmatched: source.HoldUnmatched,
	}
	// A new thread has one object. A reply to a thread that Dex never saw has more, so it is held or ignored.
	if source.TaskBotID == "" || len(p.Objects) != 1 {
		return store.Accept(ctx, event)
	}
	return store.AcceptCreating(ctx, event, source.TaskBotID, cmp.Or(p.Title, p.Type))
}
