package engine

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// Item is a frontend transcript item. Execution types stay behind this boundary.
type Item struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"`
	EntryID     int64          `json:"entryId,omitzero"`
	At          int64          `json:"at,omitzero"`
	Text        string         `json:"text,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	Origin      string         `json:"origin,omitempty"`
	Queued      bool           `json:"queued,omitzero"`
	TaskID      string         `json:"taskId,omitempty"`
	Title       string         `json:"title,omitempty"`
	Name        string         `json:"name,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
	Output      string         `json:"output,omitempty"`
	Status      string         `json:"status,omitempty"`
	DurationMs  *int64         `json:"durationMs,omitzero"`
	Model       string         `json:"model,omitempty"`
	Usage       *Usage         `json:"usage,omitzero"`
	Reason      string         `json:"reason,omitempty"`
	Aborted     bool           `json:"aborted,omitzero"`
	Boundary    string         `json:"boundary,omitempty"`
}

type Usage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

type Retry struct {
	Attempt int     `json:"attempt"`
	At      float64 `json:"at"`
	Reason  string  `json:"reason"`
}

// Event is the shared snapshot and incremental stream domain.
type Event struct {
	Type          string `json:"type"`
	Items         []Item `json:"items,omitempty"`
	Item          *Item  `json:"item,omitzero"`
	ID            string `json:"id,omitempty"`
	Text          string `json:"text,omitempty"`
	Running       bool   `json:"running,omitzero"`
	Retry         *Retry `json:"retry,omitzero"`
	HistoryBefore int64  `json:"historyBefore,omitzero"`
}

// MarshalJSON preserves required empty fields of each frontend variant.
func (e Event) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case "snapshot":
		return json.Marshal(struct {
			Type          string `json:"type"`
			Items         []Item `json:"items"`
			Running       bool   `json:"running"`
			Retry         *Retry `json:"retry,omitzero"`
			HistoryBefore int64  `json:"historyBefore,omitzero"`
		}{e.Type, e.Items, e.Running, e.Retry, e.HistoryBefore})
	case "run":
		return json.Marshal(struct {
			Type    string `json:"type"`
			Running bool   `json:"running"`
			Retry   *Retry `json:"retry,omitzero"`
		}{e.Type, e.Running, e.Retry})
	}
	type plain Event
	return json.Marshal(plain(e))
}

// MarshalJSON keeps empty text, output and arguments in the variants that require them.
func (i Item) MarshalJSON() ([]byte, error) {
	type plain Item
	switch i.Kind {
	case "user", "assistant", "thinking":
		return json.Marshal(struct {
			*plain
			Text string `json:"text"`
		}{(*plain)(&i), i.Text})
	case "tool":
		args := i.Args
		if args == nil {
			args = map[string]any{}
		}
		return json.Marshal(struct {
			*plain
			Args   map[string]any `json:"args"`
			Output string         `json:"output"`
		}{(*plain)(&i), args, i.Output})
	}
	return json.Marshal(plain(i))
}

// History is an older page in display order, with an exclusive entry cursor.
type History struct {
	Items      []Item `json:"items"`
	NextBefore int64  `json:"nextBefore,omitzero"`
}

type projection struct {
	snapshot harness.SnapshotEvent
	inbox    []harness.InboxItem
}

func (p projection) event() Event {
	e := Event{Type: "snapshot", Items: p.render(), Running: p.snapshot.Run != nil}
	if g := p.snapshot.Generation; g != nil && g.Retry != nil {
		e.Retry = &Retry{Attempt: g.Attempt, At: g.Retry.At, Reason: g.Retry.Error}
	}
	for _, entry := range p.snapshot.Entries {
		if entry.Head != nil {
			e.HistoryBefore = max(e.HistoryBefore, int64(entry.Id))
		}
	}
	return e
}

func metadata(value any) InputMetadata {
	obj, ok := value.(durable.JsonObject)
	if !ok || obj == nil {
		return InputMetadata{}
	}
	text := func(key string) string { v, _ := obj.Get(key); s, _ := v.(string); return s }
	m := InputMetadata{RequestID: text("requestId"), Content: text("content"), Kind: text("kind"), Origin: text("origin"), Source: text("source"), TaskID: text("taskId"), TaskTitle: text("taskTitle")}
	raw, _ := obj.Get("attachments")
	if list, ok := raw.([]any); ok {
		for _, v := range list {
			a, ok := v.(durable.JsonObject)
			if !ok {
				continue
			}
			str := func(k string) string { v, _ := a.Get(k); s, _ := v.(string); return s }
			size, _ := a.Get("size")
			n, _ := size.(float64)
			m.Attachments = append(m.Attachments, Attachment{ID: str("id"), Name: str("name"), Size: int64(n), MimeType: str("mimeType"), Path: str("path")})
		}
	}
	return m
}

func userItem(m InputMetadata, id string, at int64, text string) Item {
	if len(m.Attachments) > 0 {
		var paths strings.Builder
		paths.WriteString("Attached files:\n")
		for index, a := range m.Attachments {
			if index > 0 {
				paths.WriteByte('\n')
			}
			paths.WriteString("- " + a.Path)
		}
		if stripped, ok := strings.CutSuffix(text, paths.String()); ok {
			text = strings.TrimRight(stripped, " \t\r\n")
		}
	}
	if m.TaskID != "" || m.Kind == "dex.task_result" {
		return Item{Kind: "task", ID: cmp.Or(m.RequestID, id), At: at, TaskID: m.TaskID, Title: m.TaskTitle}
	}
	return Item{Kind: "user", ID: cmp.Or(m.RequestID, id), At: at, Text: text, Origin: m.Origin, Attachments: m.Attachments}
}

func userText(content ai.UserContent) string {
	switch c := content.(type) {
	case ai.UserText:
		return string(c)
	case ai.UserContentBlocks:
		var out strings.Builder
		for _, part := range c {
			if text, ok := part.(ai.TextContent); ok {
				out.WriteString(text.Text)
			} else {
				out.WriteString("[image]")
			}
		}
		return out.String()
	}
	return ""
}

func (p projection) render() []Item {
	entries := slices.Clone(p.snapshot.Entries)
	slices.SortFunc(entries, func(a, b durable.EntryRecord) int { return cmp.Compare(a.Id, b.Id) })
	results := map[string]ai.ToolResultMessage{}
	lastAssistant := -1
	for index, e := range entries {
		for _, msg := range e.Model {
			switch m := msg.(type) {
			case ai.ToolResultMessage:
				results[m.ToolCallID] = m
			case ai.AssistantMessage:
				lastAssistant = index
			}
		}
	}
	tools := map[string]harness.ToolSlot{}
	for _, t := range p.snapshot.Tools {
		tools[t.CallId] = t
	}
	items := []Item{}
	seen := map[string]bool{}
	for index, e := range entries {
		key := fmt.Sprintf("e%d", e.Id)
		var rendered []Item
		m := metadata(e.Data)
		switch e.Kind {
		case "pi.reset", "pi.compaction":
			at := int64(0)
			if obj, ok := e.Data.(durable.JsonObject); ok && obj != nil {
				v, _ := obj.Get("at")
				if n, ok := v.(float64); ok {
					at = int64(n)
				}
			}
			if len(e.Model) > 0 {
				if user, ok := e.Model[0].(ai.UserMessage); ok {
					at = user.Timestamp
				}
			}
			rendered = []Item{{Kind: "boundary", ID: key, Boundary: strings.TrimPrefix(e.Kind, "pi."), At: at}}
		case "dex.withdrawn":
			item := userItem(m, key, 0, m.Content)
			rendered = []Item{item, {Kind: "error", ID: item.ID + ":error", Reason: "Stopped before the bot read it.", Aborted: true}}
		default:
			if len(e.Model) == 0 {
				break
			}
			switch msg := e.Model[0].(type) {
			case ai.UserMessage:
				if e.Kind == "dex.user" || e.Kind == "pi.user" || e.Kind == "dex.task_result" {
					m.Kind = e.Kind
					rendered = []Item{userItem(m, key, msg.Timestamp, userText(msg.Content))}
				}
			case ai.AssistantMessage:
				rendered = assistantItems(msg, key, results, tools, p.snapshot.Run != nil && (p.snapshot.Generation == nil || p.snapshot.Generation.Message == nil) && index == lastAssistant, true)
			}
		}
		for _, item := range rendered {
			item.EntryID = int64(e.Id)
			items = append(items, item)
			seen[item.ID] = true
		}
	}
	if g := p.snapshot.Generation; g != nil && g.Message != nil {
		items = append(items, assistantItems(*g.Message, fmt.Sprintf("partial:%d", g.Message.Timestamp), results, tools, true, false)...)
	}
	for _, q := range p.inbox {
		if q.Mode == harness.InboxWrite || q.Entry == nil {
			continue
		}
		data, _ := q.Entry.Get("data")
		m := metadata(data)
		if m.RequestID == "" || seen[m.RequestID] {
			continue
		}
		kind, _ := q.Entry.Get("kind")
		m.Kind, _ = kind.(string)
		item := userItem(m, fmt.Sprint(q.Id), 0, m.Content)
		item.Queued = true
		items = append(items, item)
	}
	return items
}

func assistantItems(m ai.AssistantMessage, key string, results map[string]ai.ToolResultMessage, tools map[string]harness.ToolSlot, live, committed bool) []Item {
	out := []Item{}
	lastText := -1
	for index, block := range m.Content {
		item := Item{ID: fmt.Sprintf("%s:%d", key, index)}
		switch b := block.(type) {
		case ai.TextContent:
			item.Kind = "assistant"
			item.Text = b.Text
			lastText = len(out)
		case ai.ThinkingContent:
			item.Kind = "thinking"
			if !b.Redacted {
				item.Text = b.Thinking
			}
		case ai.ToolCall:
			item.Kind = "tool"
			item.ID = b.ID
			item.Name = b.Name
			item.Args = b.Arguments
			item.Status = "error"
			slot, hasSlot := tools[b.ID]
			if slot.Output != nil {
				item.Output = *slot.Output
			}
			if (hasSlot && slot.Status != harness.ToolSlotDone) || (live && !hasSlot) {
				item.Status = "running"
			}
			if result, ok := results[b.ID]; ok {
				item.Status = "done"
				if result.IsError {
					item.Status = "error"
				}
				item.Output = ""
				for _, part := range result.Content {
					if text, ok := part.(ai.TextContent); ok {
						item.Output += text.Text
					} else {
						item.Output += "[image]"
					}
				}
				item.DurationMs = result.DurationMs
				if b.Name == "spawn_task" && !result.IsError {
					var task struct {
						ID string `json:"id"`
					}
					if json.Unmarshal([]byte(item.Output), &task) == nil {
						item.TaskID = task.ID
					}
				}
			}
		default:
			continue
		}
		out = append(out, item)
	}
	if lastText >= 0 && committed {
		out[lastText].Model = m.Provider + "/" + m.Model
		out[lastText].Usage = &Usage{Input: m.Usage.Input, Output: m.Usage.Output}
	}
	switch m.StopReason {
	case ai.StopReasonError:
		out = append(out, Item{Kind: "error", ID: key + ":error", Reason: cmp.Or(m.ErrorMessage, "The model returned an error.")})
	case ai.StopReasonAborted:
		out = append(out, Item{Kind: "error", ID: key + ":error", Reason: "Stopped.", Aborted: true})
	case ai.StopReasonPending, ai.StopReasonStop, ai.StopReasonLength, ai.StopReasonToolUse, ai.StopReasonDeferred:
	}
	return out
}

// diff returns false when removal or reordering requires a replacement snapshot.
func diff(old, next []Item) ([]Event, bool) {
	if len(next) < len(old) {
		return nil, false
	}
	out := []Event{}
	for index, item := range next {
		if index >= len(old) {
			out = append(out, Event{Type: "item", Item: new(item)})
			continue
		}
		prev := old[index]
		if prev.ID != item.ID {
			return nil, false
		}
		if reflect.DeepEqual(prev, item) {
			continue
		}
		if (item.Kind == "assistant" || item.Kind == "thinking" || item.Kind == "user") && strings.HasPrefix(item.Text, prev.Text) {
			added := strings.TrimPrefix(item.Text, prev.Text)
			prev.Text = item.Text
			if reflect.DeepEqual(prev, item) {
				out = append(out, Event{Type: "append", ID: item.ID, Text: added})
				continue
			}
		}
		out = append(out, Event{Type: "item", Item: new(item)})
	}
	return out, true
}
