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
	Steer       bool           `json:"steer,omitzero"`
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
	// Failure is the reason of the newest failed run, on the error item that ends it. A client
	// can continue that run.
	Failure  string `json:"failure,omitempty"`
	Boundary string `json:"boundary,omitempty"`
	// Truncated marks a tool output or a thought that brief left out. Detail returns all of it.
	Truncated bool `json:"truncated,omitzero"`
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
	Phase         string `json:"phase,omitempty"`
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
			Phase         string `json:"phase,omitempty"`
			HistoryBefore int64  `json:"historyBefore,omitzero"`
		}{e.Type, e.Items, e.Running, e.Retry, e.Phase, e.HistoryBefore})
	case "run":
		return json.Marshal(struct {
			Type    string `json:"type"`
			Running bool   `json:"running"`
			Retry   *Retry `json:"retry,omitzero"`
			Phase   string `json:"phase,omitempty"`
		}{e.Type, e.Running, e.Retry, e.Phase})
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
	case "task":
		// A background command result has no child session, so its task ID is empty.
		return json.Marshal(struct {
			*plain
			TaskID string `json:"taskId"`
			Title  string `json:"title"`
		}{(*plain)(&i), i.TaskID, i.Title})
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
	entries []durable.EntryRecord
	live    harness.LiveState
	inbox   []harness.InboxItem
}

func (p projection) event() Event {
	e := Event{Type: "snapshot", Items: brief(p.render()), Running: p.live.Run != nil, Phase: executionPhase(p.live)}
	if g := p.live.Generation; g != nil && g.Retry != nil {
		e.Retry = &Retry{Attempt: g.Attempt, At: g.Retry.At, Reason: g.Retry.Error}
	}
	for _, entry := range p.entries {
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
		text = stripAttachedFiles(text, m.Attachments)
	}
	// A withdrawn input keeps the kind of the input: task_result. An input in the log has dex.task_result.
	// A background wake-up has no TaskID, so the kind decides.
	if m.TaskID != "" || m.Kind == "dex.task_result" || m.Kind == "task_result" {
		item := Item{Kind: "task", ID: cmp.Or(m.RequestID, id), At: at, TaskID: m.TaskID, Title: m.TaskTitle}
		// The answer stays out of the projection. The user reads it in the child session.
		var body struct {
			Result struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
				Status  string `json:"status"`
			} `json:"task-result"`
		}
		if json.Unmarshal([]byte(text), &body) == nil {
			item.Status, item.Reason = cmp.Or(body.Result.Outcome, body.Result.Status), body.Result.Reason
		}
		return item
	}
	return Item{Kind: "user", ID: cmp.Or(m.RequestID, id), At: at, Text: text, Origin: m.Origin, Attachments: m.Attachments}
}

// stripAttachedFiles removes the file list that inputText appends. Older entries list workspace
// paths, newer ones absolute paths, so each line only has to end with the workspace path.
func stripAttachedFiles(text string, attachments []Attachment) string {
	head, list, ok := strings.CutLast(text, "Attached files:\n")
	if !ok {
		return text
	}
	lines := strings.Split(list, "\n")
	if len(lines) != len(attachments) {
		return text
	}
	for i, a := range attachments {
		if !strings.HasPrefix(lines[i], "- ") || !strings.HasSuffix(lines[i], a.Path) {
			return text
		}
	}
	return strings.TrimRight(head, " \t\r\n")
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
	entries := slices.Clone(p.entries)
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
	for _, t := range p.live.Tools {
		tools[t.CallId] = t
	}
	items := []Item{}
	seen := map[string]bool{}
	runError := -1
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
			text := ""
			if len(e.Model) > 0 {
				if user, ok := e.Model[0].(ai.UserMessage); ok {
					at = user.Timestamp
					text = userText(user.Content)
				}
			}
			if e.Kind == "pi.compaction" {
				text = strings.TrimSuffix(strings.TrimPrefix(text, summaryPrefix), summarySuffix)
			}
			rendered = []Item{{Kind: "boundary", ID: key, Boundary: strings.TrimPrefix(e.Kind, "pi."), At: at, Text: text}}
		case CompactionNoteKind:
			note, err := durable.FromJsonValue[compactionNote](e.Data)
			if err != nil {
				break
			}
			rendered = []Item{{Kind: "boundary", ID: key, Boundary: "compaction", Status: note.Outcome, Reason: note.Error}}
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
				rendered = assistantItems(msg, key, results, tools, p.live.Run != nil && (p.live.Generation == nil || p.live.Generation.Message == nil) && index == lastAssistant, true)
			}
		}
		for _, item := range rendered {
			item.EntryID = int64(e.Id)
			items = append(items, item)
			seen[item.ID] = true
		}
		if n := len(rendered); n > 0 && rendered[n-1].Kind == "error" && e.Kind != "dex.withdrawn" {
			runError = len(items) - 1
		}
	}
	if f := p.live.RunFailure; f != nil && p.live.Run == nil && failureCurrent(entries, f) {
		if runError >= 0 && runError == len(items)-1 {
			items[runError].Failure = string(f.Reason)
		} else if item, ok := failureItem(*f); ok {
			items = append(items, item)
		}
	}
	if g := p.live.Generation; g != nil && g.Message != nil {
		items = append(items, assistantItems(*g.Message, fmt.Sprintf("partial:%d", g.Message.Timestamp), results, tools, true, false)...)
	}
	for _, c := range p.live.Compactions {
		items = append(items, Item{Kind: "boundary", ID: fmt.Sprintf("compaction:%d", c.TaskId), Boundary: "compaction", Status: "pending"})
	}
	for _, q := range p.inbox {
		if q.Entry == nil {
			continue
		}
		if q.Mode == harness.InboxWrite {
			// A summary made during a run waits here for the run's next boundary. Stop drops it.
			if kind, _ := q.Entry.Get("kind"); kind == durable.CompactionEntry.Kind {
				items = append(items, Item{Kind: "boundary", ID: fmt.Sprintf("compaction-write:%d", q.Id), Boundary: "compaction", Status: "queued"})
			}
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
		item.Steer = q.Mode == harness.InboxSteer
		items = append(items, item)
	}
	return items
}

// The wrapper PiG puts around a compaction summary in the model context.
const (
	summaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	summarySuffix = "\n</summary>"
)

// failureCurrent reports whether the newest run failure still ends the transcript: no later run
// answered and no reset followed it.
func failureCurrent(entries []durable.EntryRecord, f *harness.RunFailure) bool {
	for _, e := range entries {
		if len(e.Model) == 0 || e.ByTaskId == nil || *e.ByTaskId <= f.TaskId {
			continue
		}
		if _, ok := e.Model[0].(ai.AssistantMessage); ok {
			return false
		}
	}
	return len(entries) == 0 || entries[len(entries)-1].Kind != durable.ResetEntry.Kind
}

// failureItem is the error item of a failed run that left no error answer, for example a run that
// was stopped in a tool. A reset ends a run on purpose and shows its own boundary.
func failureItem(f harness.RunFailure) (Item, bool) {
	item := Item{Kind: "error", ID: fmt.Sprintf("failure:%d", f.TaskId), Failure: string(f.Reason)}
	detail := ""
	if f.Detail != nil {
		detail = *f.Detail
	}
	switch f.Reason {
	case harness.RunReset:
		return Item{}, false
	case harness.RunAborted:
		item.Reason, item.Aborted = "Stopped.", true
	case harness.RunNothingToAnswer:
		item.Reason = "Nothing to continue. The last reply is already complete."
	case harness.RunNoModel:
		item.Reason = "No model is available. Choose a model, then retry."
	case harness.RunModelError:
		item.Reason = cmp.Or(detail, "The model returned an error.")
	case harness.RunFaulted, harness.RunOrphaned, harness.RunInterrupted, harness.RunRestartLimit, harness.RunTurnLimit:
		item.Reason = cmp.Or(detail, "The run ended before it answered.") + " (" + string(f.Reason) + ")"
	}
	return item, true
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

// detailLimit is the longest tool output or thought that a snapshot or a history page carries.
// The transcript shows these bodies only in an opened step, and they are most of a long chat.
const detailLimit = 2048

// sessionCalls are the tools whose result the web thread card reads (web/src/lib/threads.ts).
var sessionCalls = []string{"session_new", "session_fork", "session_send", "spawn_task"}

// brief leaves out long bodies that the transcript shows only in an opened step. A thought keeps
// its first line, which titles its step. A failed call opens by itself and a session call shows
// its result in a card, so both keep their output. A streaming thought has no entry and stays.
func brief(items []Item) []Item {
	for i := range items {
		item := &items[i]
		switch {
		case item.Kind == "tool" && item.Status == "done" && len(item.Output) > detailLimit && !slices.Contains(sessionCalls, item.Name):
			item.Output, item.Truncated = "", true
		case item.Kind == "thinking" && item.EntryID != 0 && len(item.Text) > detailLimit:
			line, _, _ := strings.Cut(item.Text, "\n")
			item.Text, item.Truncated = cutRunes(line, 200), true
		}
	}
	return items
}

func cutRunes(text string, n int) string {
	for i := range text {
		if n == 0 {
			return text[:i]
		}
		n--
	}
	return text
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
