package chat

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/engine"
)

var errChannelToolCaller = errors.New("channel management needs a live owner input in Main or a Chat")

// ChannelToolCaller returns the session of a live owner call. Web and paired private chats are equivalent.
func (s *Service) ChannelToolCaller(ctx context.Context, r engine.TaskContext) (int64, error) {
	conv, source, err := liveToolSource(ctx, s.q, r)
	if err != nil || conv.ParentID.Valid || !Writable(conv.ChannelKey) || source.Kind != "message" || !userOrigin(source.Origin) {
		return 0, errChannelToolCaller
	}
	return conv.ID, nil
}
