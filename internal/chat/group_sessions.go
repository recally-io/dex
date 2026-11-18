package chat

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ToolGroupSession cannot use the private session list, search or bot resolver.
func (s *Service) ToolGroupSession(ctx context.Context, r engine.TaskContext, args engine.GroupSessionArgs) (engine.GroupSessionResult, error) {
	if r.Scope == "" {
		return engine.GroupSessionResult{}, ErrGroupUnauthorized
	}
	parent, err := s.sessionParent(ctx, r)
	if err != nil {
		return engine.GroupSessionResult{}, err
	}
	var ref engine.SessionRef
	switch args.Action {
	case "start":
		ref, err = s.newSession(ctx, r, engine.SessionNewArgs{Title: args.Title, Message: args.Message})
	case "send":
		ref, err = s.sendSession(ctx, r, engine.SessionSendArgs{ID: args.ID, Message: args.Message, Mode: args.Mode})
	case "stop":
		ref, err = s.stopSession(ctx, r, args.ID)
	case "read":
		child, err := s.childOf(ctx, s.q, parent, args.ID)
		if err != nil {
			return engine.GroupSessionResult{}, err
		}
		page, err := s.readSession(ctx, child, engine.SessionReadArgs{ID: args.ID})
		return engine.GroupSessionResult{Page: &page}, err
	case "list":
		children, err := s.q.ListAudienceChildren(ctx, sqlc.ListAudienceChildrenParams{ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}, AgentID: parent.AgentID, ExecutionScope: parent.ExecutionScope, ScopeContext: parent.ScopeContext})
		if err != nil {
			return engine.GroupSessionResult{}, err
		}
		out := engine.GroupSessionResult{Sessions: make([]engine.SessionRef, 0, len(children))}
		for _, child := range children {
			ref, err := s.ref(ctx, s.q, child)
			if err != nil {
				return engine.GroupSessionResult{}, err
			}
			out.Sessions = append(out.Sessions, ref)
		}
		return out, nil
	default:
		return engine.GroupSessionResult{}, fmt.Errorf("%w: group action is start, list, read, send or stop", errSessionInvalid)
	}
	if err != nil {
		return engine.GroupSessionResult{}, err
	}
	return engine.GroupSessionResult{Session: &ref}, nil
}
