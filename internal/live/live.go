// Package live streams the chat events of api/openapi.yaml to the web UI. Each stream has its own
// worker watch, so each browser tab gets its own snapshot, and closing one tab cannot stop another.
//
// The worker's chat events hold the transcript. Messages that the worker does not have yet, or
// never got, are only in control.sqlite; a stream adds them as user items (queued) and error items.
package live

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

const (
	// A stream that falls this many inputs behind is closed; the browser reconnects and gets a
	// snapshot. The worker's reader never waits for a stream.
	queueSize = 256
	heartbeat = 15 * time.Second
)

// Hub routes worker chat events and chat changes to the open streams.
type Hub struct {
	q       *sqlc.Queries
	chats   *chat.Service
	worker  chat.Caller
	agentID string

	mu      sync.Mutex
	byWatch map[string]*stream
	byChat  map[int64]map[*stream]struct{}
}

// New returns a Hub. Connect the worker's chat events to HandleChat, its exits to WorkerExited, and
// the chat service's changes to Changed.
func New(q *sqlc.Queries, chats *chat.Service, w chat.Caller, agentID string) *Hub {
	return &Hub{
		q: q, chats: chats, worker: w, agentID: agentID,
		byWatch: map[string]*stream{}, byChat: map[int64]map[*stream]struct{}{},
	}
}

type input struct {
	events  []jsontext.Value // chat events from the worker
	end     bool             // the worker stopped the watch
	changed bool             // the chat's messages changed in control.sqlite
}

type stream struct {
	chat chat.Chat
	in   chan input
	kill chan struct{}
	once sync.Once

	// Used only by the goroutine that serves the stream.
	watchID      string
	seen         map[string]bool   // IDs of the user items that the worker sent
	extra        map[string]string // control's items by ID, as sent
	snapshotSent bool
}

func (s *stream) send(in input) {
	select {
	case s.in <- in:
	default:
		s.close()
	}
}

func (s *stream) close() { s.once.Do(func() { close(s.kill) }) }

// HandleChat routes a worker chat event to its stream. It does not block.
func (h *Hub) HandleChat(ev worker.ChatStream) {
	h.mu.Lock()
	s := h.byWatch[ev.WatchID]
	h.mu.Unlock()
	if s != nil {
		s.send(input{events: ev.Events, end: ev.End})
	}
}

// Changed tells the streams of chatID that its messages changed. It does not block.
func (h *Hub) Changed(chatID int64) {
	h.mu.Lock()
	streams := slices.Collect(maps.Keys(h.byChat[chatID]))
	h.mu.Unlock()
	for _, s := range streams {
		s.send(input{changed: true})
	}
}

// WorkerExited closes every stream that has a worker watch, because the watches ended with the
// process.
func (h *Hub) WorkerExited() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.byWatch {
		s.close()
		delete(h.byWatch, id)
	}
}

// Open checks that chat chatID exists and returns its event stream as server-sent events. The
// stream ends when ctx is done, when the worker restarts, or when the reader is too slow.
func (h *Hub) Open(ctx context.Context, chatID int64) (io.ReadCloser, error) {
	c, err := h.chats.Get(ctx, h.agentID, chatID)
	if err != nil {
		return nil, err
	}
	return h.open(ctx, c), nil
}

// open accepts only a chat that an entry point has authorized.
func (h *Hub) open(ctx context.Context, c chat.Chat) io.ReadCloser {
	chatID := c.ID
	s := &stream{
		chat: c, in: make(chan input, queueSize), kill: make(chan struct{}),
		seen: map[string]bool{}, extra: map[string]string{},
	}
	h.mu.Lock()
	if h.byChat[chatID] == nil {
		h.byChat[chatID] = map[*stream]struct{}{}
	}
	h.byChat[chatID][s] = struct{}{}
	h.mu.Unlock()

	r, w := io.Pipe()
	go func() {
		err := h.serve(ctx, s, w)
		h.detach(ctx, s)
		_ = w.CloseWithError(err)
	}()
	return r
}

func (h *Hub) detach(ctx context.Context, s *stream) {
	h.mu.Lock()
	delete(h.byChat[s.chat.ID], s)
	if len(h.byChat[s.chat.ID]) == 0 {
		delete(h.byChat, s.chat.ID)
	}
	watched := h.byWatch[s.watchID] == s
	delete(h.byWatch, s.watchID)
	h.mu.Unlock()
	if watched {
		// The request is gone, so its context is done.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := h.worker.Call(ctx, "unwatch", worker.UnwatchParams{WatchID: s.watchID}, &struct{}{}); err != nil {
			slog.Debug("unwatch", "watch", s.watchID, "err", err)
		}
	}
}

func (h *Hub) serve(ctx context.Context, s *stream, w io.Writer) error {
	// Without a worker conversation, the chat has only control's messages.
	if err := h.attach(ctx, s); err != nil {
		return err
	}
	if s.watchID == "" {
		if err := h.writeSnapshot(ctx, s, w, nil, false, nil, nil); err != nil {
			return err
		}
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.kill:
			return nil
		case <-tick.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return err
			}
		case in := <-s.in:
			if in.end {
				return nil
			}
			if err := h.handle(ctx, s, w, in); err != nil {
				return err
			}
		}
	}
}

// attach starts the worker watch when the chat has a worker conversation and s has no watch yet.
// The snapshot comes as the first input.
func (h *Hub) attach(ctx context.Context, s *stream) error {
	if s.watchID != "" {
		return nil
	}
	c, err := h.q.GetConversation(ctx, s.chat.ID)
	if err != nil || c.WorkerConversationID.Int64 == 0 {
		return err
	}
	s.chat.WorkerConversationID = c.WorkerConversationID.Int64
	s.watchID = uuid.NewV7().String()
	h.mu.Lock()
	h.byWatch[s.watchID] = s
	h.mu.Unlock()
	return h.worker.Call(ctx, "watch", worker.WatchParams{
		WatchID: s.watchID, AgentID: h.agentID, ConversationID: c.WorkerConversationID.Int64,
	}, &struct{}{})
}

// event holds the fields of a chat event that the stream reads.
type event struct {
	Type          string           `json:"type"`
	Items         []jsontext.Value `json:"items"`
	Running       bool             `json:"running"`
	Retry         jsontext.Value   `json:"retry,omitzero"`
	HistoryBefore *int64           `json:"historyBefore,omitempty"`
	Item          *itemHead        `json:"item"`
}

type itemHead struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	At      int64  `json:"at"`
	EntryID *int64 `json:"entryId,omitempty"`
}

func (h *Hub) handle(ctx context.Context, s *stream, w io.Writer, in input) error {
	if in.changed {
		if err := h.attach(ctx, s); err != nil {
			return err
		}
		// The first snapshot reads current control state, including changes made while watch waited.
		if !s.snapshotSent {
			return nil
		}
		return h.writeExtra(ctx, s, w)
	}
	for _, raw := range in.events {
		var ev event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return fmt.Errorf("chat event: %w", err)
		}
		switch {
		case ev.Type == "snapshot":
			if err := h.writeSnapshot(ctx, s, w, ev.Items, ev.Running, ev.Retry, ev.HistoryBefore); err != nil {
				return err
			}
			continue
		case ev.Type == "item" && ev.Item != nil && ev.Item.Kind == "task":
			item, err := h.taskItem(ctx, s, *ev.Item)
			if err != nil {
				return err
			}
			raw, err = json.Marshal(struct {
				Type string         `json:"type"`
				Item jsontext.Value `json:"item"`
			}{"item", item})
			if err != nil {
				return err
			}
		case ev.Type == "item" && ev.Item != nil && ev.Item.Kind == "user":
			s.seen[ev.Item.ID] = true
			delete(s.extra, ev.Item.ID)
		}
		if err := writeEvent(w, ev.Type, raw); err != nil {
			return err
		}
	}
	return nil
}

// writeSnapshot merges unanswered messages before newer worker inputs. Queued messages stay last
// because the worker has not placed them yet.
func (h *Hub) writeSnapshot(ctx context.Context, s *stream, w io.Writer, items []jsontext.Value, running bool, retry jsontext.Value, historyBefore *int64) error {
	clear(s.seen)
	clear(s.extra)
	heads := make([]itemHead, len(items))
	for i, raw := range items {
		if err := json.Unmarshal(raw, &heads[i]); err != nil {
			return fmt.Errorf("snapshot item: %w", err)
		}
		switch heads[i].Kind {
		case "user":
			s.seen[heads[i].ID] = true
		case "task":
			var err error
			items[i], err = h.taskItem(ctx, s, heads[i])
			if err != nil {
				return err
			}
		}
	}
	if historyBefore != nil {
		open, err := h.q.ListOpenSubmissions(ctx, s.chat.ID)
		if err != nil {
			return err
		}
		var ids []string
		for _, sub := range open {
			if sub.Status == "unanswered" && !s.seen[sub.ID] {
				ids = append(ids, sub.ID)
			}
		}
		if len(ids) > 0 {
			var placed map[string]int64
			if err := h.worker.Call(ctx, "inputEntries", worker.InputEntriesParams{
				AgentID: h.agentID, ConversationID: s.chat.WorkerConversationID, RequestIDs: ids,
			}, &placed); err != nil {
				return err
			}
			// These errors load with their worker entries through history, with the same IDs.
			for id, entry := range placed {
				if entry < *historyBefore {
					s.seen[id] = true
				}
			}
		}
	}
	extra, err := h.extraItems(ctx, s)
	if err != nil {
		return err
	}
	merged := make([]jsontext.Value, 0, len(items)+len(extra))
	i := 0
	for _, x := range extra {
		for i < len(items) && (x.queued || (heads[i].Kind != "user" && heads[i].Kind != "task" && heads[i].Kind != "boundary") || heads[i].At <= x.at) {
			merged = append(merged, items[i])
			i++
		}
		merged = append(merged, x.items...)
		s.extra[x.id] = x.key
	}
	merged = append(merged, items[i:]...)
	snapshot := struct {
		Type          string           `json:"type"`
		Items         []jsontext.Value `json:"items"`
		Running       bool             `json:"running"`
		Retry         jsontext.Value   `json:"retry,omitzero"`
		HistoryBefore *int64           `json:"historyBefore,omitempty"`
	}{"snapshot", merged, running, retry, historyBefore}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	s.snapshotSent = true
	return writeEvent(w, "snapshot", raw)
}

// taskItem adds metadata owned by control. A missing record is a stream error.
func (h *Hub) taskItem(ctx context.Context, s *stream, head itemHead) (jsontext.Value, error) {
	// One query per anchor; batch by chat if snapshots get slow.
	task, err := h.q.GetTaskResultLabel(ctx, sqlc.GetTaskResultLabelParams{ResultID: head.ID, ChatID: s.chat.ID})
	if err != nil {
		return nil, fmt.Errorf("task result %s title: %w", head.ID, err)
	}
	return json.Marshal(api.TaskItem{Kind: api.TaskItemKindTask, Id: head.ID, TaskId: task.ID, At: new(head.At), Title: task.Title, EntryId: head.EntryID})
}

// writeExtra writes control's items that changed since the stream sent them.
func (h *Hub) writeExtra(ctx context.Context, s *stream, w io.Writer) error {
	extra, err := h.extraItems(ctx, s)
	if err != nil {
		return err
	}
	for _, x := range extra {
		if s.extra[x.id] == x.key {
			continue
		}
		s.extra[x.id] = x.key
		for _, item := range x.items {
			raw, err := json.Marshal(struct {
				Type string         `json:"type"`
				Item jsontext.Value `json:"item"`
			}{"item", item})
			if err != nil {
				return err
			}
			if err := writeEvent(w, "item", raw); err != nil {
				return err
			}
		}
	}
	return nil
}

// extraItem is one message that only control has: a user item, and an error item when the message
// failed.
type extraItem struct {
	id     string
	at     int64
	key    string // changes when the items change
	queued bool
	items  []jsontext.Value
}

func (h *Hub) extraItems(ctx context.Context, s *stream) ([]extraItem, error) {
	open, err := h.q.ListOpenSubmissions(ctx, s.chat.ID)
	if err != nil {
		return nil, err
	}
	var out []extraItem
	for _, sub := range open {
		if s.seen[sub.ID] {
			continue
		}
		at := sub.CreatedAt
		queued := sub.Status == "pending"
		item := api.UserItem{Kind: api.User, Id: sub.ID, Text: sub.Content, At: &at, Queued: &queued}
		var attachments []worker.Attachment
		if err := json.Unmarshal([]byte(sub.Attachments), &attachments); err != nil {
			return nil, fmt.Errorf("submission %s attachments: %w", sub.ID, err)
		}
		if len(attachments) > 0 {
			item.Attachments = &attachments
		}
		user, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		x := extraItem{id: sub.ID, at: at, key: sub.Status, queued: queued, items: []jsontext.Value{user}}
		if sub.Status == "unanswered" {
			aborted := sub.Reason.String == "aborted"
			failed, err := json.Marshal(api.ErrorItem{
				Kind: api.ErrorItemKindError, Id: sub.ID + ":error", Reason: reason(sub.Reason.String), Aborted: &aborted,
			})
			if err != nil {
				return nil, err
			}
			x.items = append(x.items, failed)
		}
		out = append(out, x)
	}
	// Keep receipt order within each group. A queued input must not move later unanswered inputs.
	slices.SortStableFunc(out, func(a, b extraItem) int {
		switch {
		case a.queued == b.queued:
			return 0
		case a.queued:
			return 1
		default:
			return -1
		}
	})
	return out, nil
}

func reason(r string) string {
	switch r {
	case "aborted":
		return "Stopped before the agent read it."
	case "":
		return "The agent did not answer."
	}
	return r
}

func writeEvent(w io.Writer, typ string, data []byte) error {
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
	return err
}

// History reads only through the worker and adds the same task names as the live stream.
func (h *Hub) History(ctx context.Context, chatID, before int64, limit int) (worker.HistoryResult, error) {
	c, err := h.chats.Get(ctx, h.agentID, chatID)
	if err != nil {
		return worker.HistoryResult{}, err
	}
	page := worker.HistoryResult{Items: []jsontext.Value{}}
	if c.WorkerConversationID == 0 {
		return page, nil
	}
	if err := h.worker.Call(ctx, "history", worker.HistoryParams{
		AgentID: h.agentID, ConversationID: c.WorkerConversationID, Before: before, Limit: limit,
	}, &page); err != nil {
		return page, err
	}
	for i, raw := range page.Items {
		var head itemHead
		if err := json.Unmarshal(raw, &head); err != nil {
			return page, err
		}
		if head.Kind == "task" {
			page.Items[i], err = h.taskItem(ctx, &stream{chat: c}, head)
			if err != nil {
				return page, err
			}
		}
	}
	return page, nil
}
