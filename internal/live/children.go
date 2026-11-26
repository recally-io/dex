package live

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"strconv"

	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

type childEvent struct {
	Type      string `json:"type"`
	SessionID int64  `json:"sessionId"`
	BotID     string `json:"botId"`
	State     string `json:"state"`
}

// childState reads current facts only. Idle is not a submission outcome.
func (h *Hub) childState(ctx context.Context, parentID, sessionID int64, botID string, running *bool) (childEvent, error) {
	c, err := h.sessions.Get(ctx, botID, sessionID)
	if err != nil {
		return childEvent{}, err
	}
	if c.ID != parentID && c.ParentID != parentID {
		return childEvent{}, session.ErrNotFound
	}
	if running == nil && c.ExecutionID != 0 {
		if run, available := h.bots.ChildRunning(engine.Ref{BotID: botID, ID: c.ExecutionID}); available {
			running = &run
		}
	}
	state := "idle"
	switch {
	case running != nil && *running:
		state = "running"
	case !c.Busy:
	case c.ExecutionID == 0 || running != nil:
		state = "waiting"
	default:
		state = "unavailable"
	}
	return childEvent{Type: "child", SessionID: sessionID, BotID: botID, State: state}, nil
}

// Only completed session tools identify children. Streamed arguments do not prove creation.
func childRef(item engine.Item, botID string) (int64, string) {
	if item.Kind != "tool" || item.Status != "done" {
		return 0, ""
	}
	switch item.Name {
	case "session_new", "session_fork", "session_send":
	default:
		return 0, ""
	}
	var ref struct {
		ID    string `json:"id"`
		BotID string `json:"botId"`
	}
	if json.Unmarshal([]byte(item.Output), &ref) != nil {
		return 0, ""
	}
	id, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil || id <= 0 {
		return 0, ""
	}
	return id, cmp.Or(ref.BotID, botID)
}

func (h *Hub) subscribe(id int64, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bySession[id] == nil {
		h.bySession[id] = map[*subscriber]struct{}{}
	}
	h.bySession[id][sub] = struct{}{}
}

func (h *Hub) unsubscribe(id int64, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.bySession[id], sub)
	if len(h.bySession[id]) == 0 {
		delete(h.bySession, id)
	}
}
