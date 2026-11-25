package chat

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/engine"
)

var errChannelToolCaller = errors.New("channel management needs a web owner input in Main or a Chat")

// ChannelToolCaller returns the business session of a live owner call. Main also
// accepts channel inputs, so its session key alone cannot grant owner access.
func (s *Service) ChannelToolCaller(ctx context.Context, r engine.TaskContext) (int64, error) {
	if err := s.ToolBotCaller(ctx, r, true); err != nil {
		return 0, errChannelToolCaller
	}
	source, err := liveSource(ctx, s.q, r)
	if err != nil || source.Origin != "web" {
		return 0, errChannelToolCaller
	}
	// A steer can join an existing run. Do not give a mixed-origin run owner access.
	for _, id := range r.RunInputIDs {
		input, err := s.q.GetSubmission(ctx, id)
		if err != nil || input.ConversationID != source.ConversationID || input.Origin != "web" || input.Kind != "message" {
			return 0, errChannelToolCaller
		}
	}
	return source.ConversationID, nil
}
