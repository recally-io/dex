package chat

import (
	"context"
	"log/slog"

	"github.com/recally-io/dex/internal/engine"
)

// SessionExecution carries state, never a child transcript or tool arguments.
type SessionExecution struct {
	ChatID  int64  `json:"chatId"`
	AgentID string `json:"agentId"`
	engine.Execution
}

// Executions returns the selected session and its delegated inputs. Each card follows its own
// submission, so a later input to the same child cannot change an earlier card's outcome.
func (s *Service) Executions(ctx context.Context, agentID string, chatID int64) ([]SessionExecution, error) {
	if _, err := s.Get(ctx, agentID, chatID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListExecutionTurns(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := []SessionExecution{}
	for start := 0; start < len(rows); {
		end := start + 1
		for end < len(rows) && rows[end].ChatID == rows[start].ChatID {
			end++
		}
		group := rows[start:end]
		state := SessionExecution{ChatID: group[0].ChatID, AgentID: group[0].AgentID, Phase: "idle", Turns: []engine.ExecutionTurn{}}
		pending := []string{}
		for _, row := range group {
			if row.Status == "pending" {
				pending = append(pending, row.ID)
			}
		}
		native := engine.Execution{Phase: "idle"}
		if len(pending) > 0 || group[0].ChatID == chatID {
			if len(pending) > 0 {
				native.Phase = "waiting"
			}
			if group[0].ExecutionID.Valid {
				native, err = s.engine.Execution(ctx, engine.Ref{AgentID: state.AgentID, ID: group[0].ExecutionID.Int64}, pending)
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					// Admission or reload can make execution temporarily unavailable. Never turn that into idle.
					slog.Warn("read session execution state", "chat", state.ChatID, "error", err)
					native = engine.Execution{Phase: "unavailable"}
				}
			}
		}
		state.Phase, state.Queued = native.Phase, native.Queued
		byRequest := map[string]engine.ExecutionTurn{}
		for _, turn := range native.Turns {
			byRequest[turn.ID] = turn
		}
		for _, row := range group {
			turn := engine.ExecutionTurn{ID: row.ID, Status: "waiting"}
			switch row.Status {
			case "done":
				turn.Status = "done"
			case "unanswered":
				turn.Status = "failed"
				turn.Reason = row.Reason.String
			default:
				if observed, ok := byRequest[row.ID]; ok {
					turn = observed
				} else if state.Phase == "unavailable" {
					turn.Status = "unavailable"
				}
			}
			state.Turns = append(state.Turns, turn)
		}
		out = append(out, state)
		start = end
	}
	return out, nil
}
