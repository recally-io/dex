package server

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/recally-io/dex/internal/api"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func (h *handler) StreamTaskEvents(ctx context.Context, req api.StreamTaskEventsRequestObject) (api.StreamTaskEventsResponseObject, error) {
	after := req.Params.After
	if header := req.Params.LastEventID; header != nil && *header != "" {
		cursor, err := strconv.ParseInt(*header, 10, 64)
		if err != nil {
			return nil, errInvalid
		}
		after = &cursor
	}
	if after != nil && *after < 0 {
		return nil, errInvalid
	}
	return taskStream{ctx: ctx, tasks: h.Tasks, broker: h.TaskEvents, after: after}, nil
}

type taskStream struct {
	ctx    context.Context
	tasks  *tasksvc.Service
	broker *tasksvc.Broker
	after  *int64
}

func (s taskStream) VisitStreamTaskEventsResponse(w http.ResponseWriter) error {
	// Subscribe before the first read. Hints can arrive out of order or be dropped.
	hints, unsubscribe := s.broker.Subscribe()
	defer unsubscribe()
	var cursor int64
	if s.after == nil {
		head, err := s.tasks.TaskEventHead(s.ctx)
		if err != nil {
			return err
		}
		cursor = head
	} else {
		cursor = *s.after
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	send := func(frame string) error {
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprint(w, frame); err != nil {
			return err
		}
		return rc.Flush()
	}
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	drain := func() (bool, error) {
		sent := false
		for {
			rows, err := s.tasks.TaskEventsAfter(s.ctx, cursor)
			if err != nil {
				return false, err
			}
			if len(rows) == 0 {
				return sent, nil
			}
			for _, row := range rows {
				data, err := json.Marshal(api.TaskEvent{TaskId: row.TaskID})
				if err != nil {
					return false, err
				}
				if err := send(fmt.Sprintf("id: %d\nevent: task_entry\ndata: %s\n\n", row.ID, data)); err != nil {
					return false, err
				}
				cursor = row.ID
				sent = true
			}
		}
	}
	if _, err := drain(); err != nil {
		return err
	}
	if err := send(fmt.Sprintf("id: %d\nevent: task_ready\ndata: {}\n\n", cursor)); err != nil {
		return err
	}

	for {
		select {
		case <-s.ctx.Done():
			return nil
		case <-tick.C:
			if err := send(": keepalive\n\n"); err != nil {
				return err
			}
		case <-hints:
			// Clear only the hints already queued. A later commit needs a new read.
			for range len(hints) {
				<-hints
			}
			sent, err := drain()
			if err != nil {
				return err
			}
			if !sent {
				// Questions and Session state can change without a ledger entry.
				if err := send("event: task_refresh\ndata: {}\n\n"); err != nil {
					return err
				}
			}
		}
	}
}
