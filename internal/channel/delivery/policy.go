package delivery

import (
	"context"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

// Policy supplies rules for one platform. Without a policy, the manager uses
// shared commands, shared reply handling, and the platform's default reactions.
type Policy interface {
	// AdmitInput checks the peer before input validation. Receive then checks
	// for a duplicate source before it looks up a stored command. The adapter
	// checks the actor before it downloads data.
	AdmitInput(peer string) error
	// IsCommand recognizes additional commands for a source with no receipt.
	IsCommand(text string) bool
	// RunCommand checks the target and actor after the manager takes ownership
	// of the command receipt. It may run a shared command on the authorized
	// target. Command effects must check authorization again inside the writer
	// transaction. An empty answer is silent. The manager saves the answer and
	// wakes delivery if wake is true.
	RunCommand(ctx context.Context, command sqlc.ChannelCommand, runShared func(context.Context, int64) (string, error)) (answer string, wake bool, err error)

	// DecorateCommandReply and DecorateReply add platform data before the
	// manager freezes a message. A send retry uses the frozen data. The manager
	// checks that the Outbox row is still pending immediately before each send.
	DecorateCommandReply(ctx context.Context, command sqlc.ChannelCommand, target session.Session, part *channel.Part) error
	DecorateReply(ctx context.Context, message sqlc.OutboxMessage, part *channel.Part) error
	// Confirmed records platform references in the same transaction as the receipt.
	Confirmed(ctx context.Context, q *sqlc.Queries, message sqlc.OutboxMessage, part channel.Part, receipt channel.Receipt) error

	// AcknowledgeQueue owns any special handling of the initial queued reaction.
	// nativeQueued reports whether the input is queued and whether admission
	// has published its native handle. An unknown reaction outcome stops observation.
	AcknowledgeQueue(ctx context.Context, bot channel.Reactor, peer, submissionID string, source session.ChannelContext, nativeQueued func(context.Context) (queued, published bool)) (handled bool, err error)
	// ReactionAllowed runs immediately before a normal status reaction.
	ReactionAllowed(ctx context.Context, peer string, source session.ChannelContext) bool
}

func (m *Manager) policy(peer string) Policy {
	platform, _, ok := strings.Cut(peer, ":")
	if !ok {
		return nil
	}
	return m.policies[platform]
}
