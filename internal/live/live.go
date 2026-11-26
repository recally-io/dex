// Package live serves typed engine transcripts as server-sent events.
package live

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

const heartbeat = 15 * time.Second

type subscriber struct {
	signal  chan struct{}
	pending map[int64]struct{}
}

// Hub routes reference and child-state notifications to each transcript connection.
type Hub struct {
	sessions  *session.Service
	agents    *engine.Agents
	mu        sync.Mutex
	bySession map[int64]map[*subscriber]struct{}
}

func New(sessions *session.Service, agents *engine.Agents) *Hub {
	return &Hub{sessions: sessions, agents: agents, bySession: map[int64]map[*subscriber]struct{}{}}
}

// Changed coalesces notifications for the session and parents that observe this child.
func (h *Hub) Changed(sessionID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.bySession[sessionID] {
		sub.pending[sessionID] = struct{}{}
		select {
		case sub.signal <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) Open(ctx context.Context, agentID string, sessionID int64) (io.ReadCloser, error) {
	return h.open(ctx, agentID, sessionID, false)
}

func (h *Hub) OpenWeb(ctx context.Context, agentID string, sessionID int64) (io.ReadCloser, error) {
	return h.open(ctx, agentID, sessionID, true)
}

func (h *Hub) open(ctx context.Context, agentID string, sessionID int64, web bool) (io.ReadCloser, error) {
	c, err := h.sessions.Get(ctx, agentID, sessionID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	sub := &subscriber{signal: make(chan struct{}, 1), pending: map[int64]struct{}{}}
	h.subscribe(c.ID, sub)
	reader, writer := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	go func() {
		err := h.serve(ctx, agentID, c, sub, writer, web)
		h.unsubscribe(c.ID, sub)
		stop()
		cancel()
		_ = writer.CloseWithError(err)
	}()
	return &streamReader{PipeReader: reader, cancel: cancel}, nil
}

type streamReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *streamReader) Close() error { r.cancel(); return r.PipeReader.Close() }

func (h *Hub) serve(ctx context.Context, agentID string, c session.Session, sub *subscriber, writer io.Writer, web bool) error {
	children := map[int64]string{}
	states := map[int64]string{}
	defer func() {
		for id := range children {
			h.unsubscribe(id, sub)
		}
	}()
	send := func(event childEvent) error {
		if states[event.SessionID] == event.State {
			return nil
		}
		if err := writeJSONEvent(writer, "child", event); err != nil {
			return err
		}
		states[event.SessionID] = event.State
		return nil
	}
	observe := func(event engine.Event) error {
		if event.Type != "snapshot" && event.Type != "item" {
			return nil
		}
		found := map[int64]string{}
		items := event.Items
		if event.Item != nil {
			items = []engine.Item{*event.Item}
		}
		for _, item := range items {
			if id, bot := childRef(item, agentID); id != 0 && id != c.ID {
				found[id] = bot
			}
		}
		if event.Type == "snapshot" {
			for id, bot := range children {
				if _, ok := found[id]; !ok {
					h.unsubscribe(id, sub)
					delete(children, id)
					if err := send(childEvent{Type: "child", SessionID: id, AgentID: bot, State: "unavailable"}); err != nil {
						return err
					}
					delete(states, id)
				}
			}
		}
		for id, bot := range found {
			if _, ok := children[id]; ok {
				continue
			}
			// Register before the state read, so a native change during the read cannot be lost.
			h.subscribe(id, sub)
			state, err := h.childState(ctx, c.ID, id, bot, nil)
			if err != nil {
				h.unsubscribe(id, sub)
				if errors.Is(err, session.ErrNotFound) {
					continue
				}
				return err
			}
			children[id] = bot
			if err := send(state); err != nil {
				return err
			}
		}
		return nil
	}
	var watch *engine.Watch
	running := false
	ownState := func() error {
		var native *bool
		if watch != nil {
			native = &running
		}
		state, err := h.childState(ctx, c.ID, c.ID, agentID, native)
		if err != nil {
			return err
		}
		return send(state)
	}
	defer func() {
		if watch != nil {
			watch.Close()
		}
	}()
	attach := func() error {
		if watch != nil {
			return nil
		}
		latest, err := h.sessions.Get(ctx, agentID, c.ID)
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
		running = watch.Snapshot.Running
		if err := writeEvent(writer, watch.Snapshot); err != nil {
			return err
		}
		return observe(watch.Snapshot)
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
	var noticePosition int64
	notifications := func(initial bool) error {
		if !web || !session.Writable(c.ChannelKey) || c.ParentID != 0 {
			return nil
		}
		limit := -1
		if initial {
			limit = 30
		}
		page, err := h.sessions.TimelineNotifications(ctx, agentID, c.ID, "", noticePosition, limit)
		if err != nil {
			return err
		}
		if !initial && len(page.Notifications) == 0 {
			return nil
		}
		noticePosition = page.Position
		return writeJSONEvent(writer, "notifications", struct {
			Type    string `json:"type"`
			Initial bool   `json:"initial"`
			session.NotificationPage
		}{"notifications", initial, page})
	}
	if err := notifications(true); err != nil {
		return err
	}
	if err := ownState(); err != nil {
		return err
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
		case <-sub.signal:
			h.mu.Lock()
			pending := sub.pending
			sub.pending = map[int64]struct{}{}
			h.mu.Unlock()
			for id := range pending {
				if id == c.ID {
					if err := attach(); err != nil {
						return err
					}
					if err := notifications(false); err != nil {
						return err
					}
					if err := ownState(); err != nil {
						return err
					}
				} else if bot, ok := children[id]; ok {
					state, err := h.childState(ctx, c.ID, id, bot, nil)
					if err != nil {
						return err
					}
					if err := send(state); err != nil {
						return err
					}
				}
			}
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := writeEvent(writer, event); err != nil {
				return err
			}
			if err := observe(event); err != nil {
				return err
			}
			if event.Type == "snapshot" || event.Type == "run" {
				running = event.Running
				if err := ownState(); err != nil {
					return err
				}
			}
		case <-tick.C:
			if _, err := io.WriteString(writer, ": ping\n\n"); err != nil {
				return err
			}
		}
	}
}

func writeEvent(w io.Writer, event engine.Event) error {
	return writeJSONEvent(w, event.Type, event)
}

func writeJSONEvent(w io.Writer, kind string, event any) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
	return err
}

// Detail returns the full tool output or thought of one committed item.
func (h *Hub) Detail(ctx context.Context, agentID string, sessionID, entryID int64, itemID string) (engine.Item, error) {
	c, err := h.sessions.Get(ctx, agentID, sessionID)
	if err != nil {
		return engine.Item{}, err
	}
	if c.ExecutionID == 0 {
		return engine.Item{}, engine.ErrItemNotFound
	}
	return h.agents.Detail(ctx, engine.Ref{AgentID: agentID, ID: c.ExecutionID}, entryID, itemID)
}

func (h *Hub) History(ctx context.Context, agentID string, sessionID, before int64, limit int) (engine.History, error) {
	c, err := h.sessions.Get(ctx, agentID, sessionID)
	if err != nil {
		return engine.History{}, err
	}
	if c.ExecutionID == 0 {
		return engine.History{Items: []engine.Item{}}, nil
	}
	return h.agents.History(ctx, engine.Ref{AgentID: agentID, ID: c.ExecutionID}, before, limit)
}
