package session

import (
	"context"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ErrChannelUnauthorized means the channel has no authority for this operation.
var ErrChannelUnauthorized = errors.New("channel input is not authorized")

// ChannelRoute describes how a channel peer can interact with a Session.
type ChannelRoute struct {
	User  bool
	Owner bool
	Quote bool
	Files bool
}

// ChannelPolicy is bound before recovery. Write checks use the caller's writer and do not call the network.
type ChannelPolicy struct {
	CheckPeer      func(peer string) error
	CheckInputIn   func(ctx context.Context, q *sqlc.Queries, botID, peer string, input ChannelContext) error
	CheckCommandIn func(ctx context.Context, q *sqlc.Queries, source, botID string, sessionID int64) error
	Route          func(peer string) ChannelRoute
}

func (s *Service) ConfigureChannelPolicy(policy ChannelPolicy) {
	s.channelPolicy = policy
}

func (s *Service) checkChannelPeer(peer string) error {
	if s.channelPolicy.CheckPeer == nil {
		return ErrChannelUnauthorized
	}
	return s.channelPolicy.CheckPeer(peer)
}

func (s *Service) checkChannelInput(ctx context.Context, q *sqlc.Queries, botID, peer string, source ChannelContext) error {
	if s.channelPolicy.CheckInputIn == nil {
		return ErrChannelUnauthorized
	}
	return s.channelPolicy.CheckInputIn(ctx, q, botID, peer, source)
}

func (s *Service) checkChannelCommand(ctx context.Context, q *sqlc.Queries, source, botID string, sessionID int64) error {
	if s.channelPolicy.CheckCommandIn == nil {
		return ErrChannelUnauthorized
	}
	return s.channelPolicy.CheckCommandIn(ctx, q, source, botID, sessionID)
}

func (s *Service) channelRoute(peer string) ChannelRoute {
	if s.channelPolicy.Route == nil {
		return ChannelRoute{}
	}
	return s.channelPolicy.Route(peer)
}

// UserOrigin reports whether a person on the web or a channel wrote the input.
func (s *Service) UserOrigin(origin string) bool {
	return origin == "web" || s.channelRoute(origin).User
}

// ownerOrigin excludes channel routes that cannot perform owner actions.
func (s *Service) ownerOrigin(origin string) bool {
	return origin == "web" || s.channelRoute(origin).Owner
}

func isChannelOrigin(origin string) bool {
	return strings.Contains(origin, ":")
}
