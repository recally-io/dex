// Package live serves typed engine transcripts as server-sent events.
package live

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/engine"
)

const heartbeat = 15 * time.Second

// Hub keeps only notifications needed to attach an initially empty chat.
type Hub struct {
	chats  *chat.Service
	agents *engine.Agents
	mu     sync.Mutex
	byChat map[int64]map[chan struct{}]struct{}
}

func New(chats *chat.Service, agents *engine.Agents) *Hub {
	return &Hub{chats: chats, agents: agents, byChat: map[int64]map[chan struct{}]struct{}{}}
}

// Changed wakes empty chats after the business service stores their execution reference.
func (h *Hub) Changed(chatID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for signal := range h.byChat[chatID] {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) Open(ctx context.Context, agentID string, chatID int64) (io.ReadCloser, error) {
	c, err := h.chats.Get(ctx, agentID, chatID)
	if err != nil {
		return nil, err
	}
	return h.open(ctx, agentID, c), nil
}

// open accepts only a chat that the caller has authorized.
func (h *Hub) open(ctx context.Context, agentID string, c chat.Chat) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)
	signal := make(chan struct{}, 1)
	h.mu.Lock()
	if h.byChat[c.ID] == nil {
		h.byChat[c.ID] = map[chan struct{}]struct{}{}
	}
	h.byChat[c.ID][signal] = struct{}{}
	h.mu.Unlock()
	reader, writer := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	go func() {
		err := h.serve(ctx, agentID, c, signal, writer)
		h.mu.Lock()
		delete(h.byChat[c.ID], signal)
		if len(h.byChat[c.ID]) == 0 {
			delete(h.byChat, c.ID)
		}
		h.mu.Unlock()
		stop()
		cancel()
		_ = writer.CloseWithError(err)
	}()
	return &streamReader{PipeReader: reader, cancel: cancel}
}

type streamReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *streamReader) Close() error { r.cancel(); return r.PipeReader.Close() }

func (h *Hub) serve(ctx context.Context, agentID string, c chat.Chat, signal <-chan struct{}, writer io.Writer) error {
	var watch *engine.Watch
	defer func() {
		if watch != nil {
			watch.Close()
		}
	}()
	attach := func() error {
		if watch != nil {
			return nil
		}
		latest, err := h.chats.Get(ctx, agentID, c.ID)
		if err != nil {
			return err
		}
		if latest.ExecutionID == 0 {
			return nil
		}
		watch, err = h.agents.Watch(ctx, engine.Ref{AgentID: agentID, ID: latest.ExecutionID})
		if err != nil {
			return err
		}
		return writeEvent(writer, watch.Snapshot)
	}
	// Register before rereading the reference, so admission cannot be missed.
	if err := attach(); err != nil {
		return err
	}
	if watch == nil {
		if err := writeEvent(writer, engine.Event{Type: "snapshot", Items: []engine.Item{}}); err != nil {
			return err
		}
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		var events <-chan engine.Event
		if watch != nil {
			events = watch.Events
		}
		select {
		case <-ctx.Done():
			return nil
		case <-signal:
			if err := attach(); err != nil {
				return err
			}
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := writeEvent(writer, event); err != nil {
				return err
			}
		case <-tick.C:
			if _, err := io.WriteString(writer, ": ping\n\n"); err != nil {
				return err
			}
		}
	}
}

func writeEvent(w io.Writer, event engine.Event) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, raw)
	return err
}

func (h *Hub) History(ctx context.Context, agentID string, chatID, before int64, limit int) (engine.History, error) {
	c, err := h.chats.Get(ctx, agentID, chatID)
	if err != nil {
		return engine.History{}, err
	}
	if c.ExecutionID == 0 {
		return engine.History{Items: []engine.Item{}}, nil
	}
	return h.agents.History(ctx, engine.Ref{AgentID: agentID, ID: c.ExecutionID}, before, limit)
}
