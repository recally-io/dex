package live

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"strconv"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/engine"
)

type childEvent struct {
	Type    string `json:"type"`
	ChatID  int64  `json:"chatId"`
	AgentID string `json:"agentId"`
	State   string `json:"state"`
}

// childState reads current facts only. Idle is not a submission outcome.
func (h *Hub) childState(ctx context.Context, parentID, chatID int64, agentID string, running *bool) (childEvent, error) {
	c, err := h.chats.Get(ctx, agentID, chatID)
	if err != nil {
		return childEvent{}, err
	}
	if c.ID != parentID && c.ParentID != parentID {
		return childEvent{}, chat.ErrNotFound
	}
	if running == nil && c.ExecutionID != 0 {
		if run, available := h.agents.ChildRunning(engine.Ref{AgentID: agentID, ID: c.ExecutionID}); available {
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
	return childEvent{Type: "child", ChatID: chatID, AgentID: agentID, State: state}, nil
}

// Only completed session tools identify children. Streamed arguments do not prove creation.
func childRef(item engine.Item, agentID string) (int64, string) {
	if item.Kind != "tool" || item.Status != "done" {
		return 0, ""
	}
	switch item.Name {
	case "session_new", "session_fork", "session_send", "spawn_task":
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
	return id, cmp.Or(ref.BotID, agentID)
}

func (h *Hub) subscribe(id int64, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byChat[id] == nil {
		h.byChat[id] = map[*subscriber]struct{}{}
	}
	h.byChat[id][sub] = struct{}{}
}

func (h *Hub) unsubscribe(id int64, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.byChat[id], sub)
	if len(h.byChat[id]) == 0 {
		delete(h.byChat, id)
	}
}
