package engine

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// Item is a frontend transcript item. Execution types stay behind this boundary.
type Item struct {
	Kind           string       `json:"kind"`
	ID             string       `json:"id"`
	EntryID        int64        `json:"entryId,omitzero"`
	At             int64        `json:"at,omitzero"`
	Text           string       `json:"text,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	Origin         string       `json:"origin,omitempty"`
	Queued         bool         `json:"queued,omitzero"`
	Steer          bool         `json:"steer,omitzero"`
	ChildSessionID int64        `json:"childSessionId,omitzero"`
	Title          string       `json:"title,omitempty"`
	// Alert marks a legacy Task alert on Main.
	Alert      bool           `json:"alert,omitzero"`
	Name       string         `json:"name,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
	Output     string         `json:"output,omitempty"`
	Status     string         `json:"status,omitempty"`
	DurationMs *int64         `json:"durationMs,omitzero"`
	Model      string         `json:"model,omitempty"`
	Usage      *Usage         `json:"usage,omitzero"`
	Reason     string         `json:"reason,omitempty"`
	Aborted    bool           `json:"aborted,omitzero"`
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
	RunTarget     string `json:"runTarget,omitempty"`
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
			RunTarget     string `json:"runTarget,omitempty"`
			Retry         *Retry `json:"retry,omitzero"`
			HistoryBefore int64  `json:"historyBefore,omitzero"`
		}{e.Type, e.Items, e.Running, e.RunTarget, e.Retry, e.HistoryBefore})
	case "run":
		return json.Marshal(struct {
			Type      string `json:"type"`
			Running   bool   `json:"running"`
			RunTarget string `json:"runTarget,omitempty"`
			Retry     *Retry `json:"retry,omitzero"`
		}{e.Type, e.Running, e.RunTarget, e.Retry})
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
	hideToolArguments func(string) bool
	entries           []durable.EntryRecord
	live              harness.LiveState
	inbox             []harness.InboxItem
}

func (p projection) event() Event {
	e := Event{Type: "snapshot", Items: brief(p.render()), Running: p.live.Run != nil}
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

func metadata(value any) inputMetadata {
	obj, ok := value.(durable.JsonObject)
	if !ok || obj == nil {
		return inputMetadata{}
	}
	text := func(key string) string { v, _ := obj.Get(key); s, _ := v.(string); return s }
	m := inputMetadata{RequestID: text("requestId"), Content: text("content"), Kind: text("kind"), Origin: text("origin"), Source: text("source"), Title: text("taskTitle")}
	flag, _ := obj.Get("hasTask")
	m.HasTask, _ = flag.(bool)
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

// inputKindForEntry reads the native kind, including the old combined entry. The
// metadata kind is needed for withdrawn inputs and saved Board wake receipts.
func inputKindForEntry(kind string, m inputMetadata) string {
	switch kind {
	case "dex.session_result", "dex.wake", "dex.task_wake":
		return strings.TrimPrefix(kind, "dex.")
	}
	if kind != legacyResultEntry && m.Kind != legacyResultKind {
		return m.Kind
	}
	if m.Kind == "task_wake" {
		return "task_wake"
	}
	id := strings.ToLower(m.RequestID)
	if strings.HasPrefix(id, "background:") || strings.HasPrefix(id, "monitor:") || m.Origin == "monitor" {
		return "wake"
	}
	return "session_result"
}

func inputItem(m inputMetadata, id string, at int64, text string) Item {
	item := Item{ID: cmp.Or(m.RequestID, id), At: at}
	switch m.Kind {
	case "task_wake":
		item.Kind, item.Title = "task_wake", "Task wake"
	case "session_result":
		item.Kind = "session_result"
		item.Alert = m.HasTask || strings.HasPrefix(m.RequestID, "task-alert:")
		// Read only display fields. The answer stays in the child session.
		type result struct {
			Title   string `json:"title"`
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
			Session string `json:"session"`
		}
		var body struct {
			Result       *result `json:"session-result"`
			LegacyResult *result `json:"task-result"`
		}
		item.Title = cmp.Or(m.Title, "Session result")
		if json.Unmarshal([]byte(cmp.Or(m.Content, text)), &body) == nil {
			if r := cmp.Or(body.Result, body.LegacyResult); r != nil {
				item.Title, item.Status, item.Reason = cmp.Or(m.Title, r.Title, item.Title), r.Outcome, r.Reason
				if child, err := strconv.ParseInt(r.Session, 10, 64); err == nil && child > 0 {
					item.ChildSessionID = child
				}
			}
		}
	case "wake":
		item.Kind = "wake"
		text = cmp.Or(m.Content, text)
		if strings.HasPrefix(strings.ToLower(m.RequestID), "background:") {
			var body struct {
				Process struct {
					Label  string `json:"label"`
					Event  string `json:"event"`
					Notice string `json:"notice"`
				} `json:"background-result"`
			}
			item.Title = cmp.Or(m.Title, "Process")
			if json.Unmarshal([]byte(text), &body) == nil {
				item.Title = cmp.Or(m.Title, body.Process.Label, item.Title)
				item.Status, item.Reason = body.Process.Event, body.Process.Notice
			}
		} else {
			item.Title = cmp.Or(m.Title, "Monitor")
			firstLine, _, _ := strings.Cut(text, "\n")
			if header, ok := strings.CutPrefix(firstLine, "[Monitor "); ok && strings.HasSuffix(header, "]") {
				if status, _, ok := strings.Cut(header, ": "); ok {
					item.Status = status
				}
			}
		}
	default:
		if m.HasTask {
			text = m.Content
		} else if len(m.Attachments) > 0 {
			text = stripAttachedFiles(text, m.Attachments)
		}
		item.Kind, item.Text, item.Origin, item.Attachments = "user", text, m.Origin, m.Attachments
	}
	return item
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
		case runnerNoticeKind:
			if len(e.Model) == 0 {
				break
			}
			if user, ok := e.Model[0].(ai.UserMessage); ok {
				rendered = []Item{{Kind: "boundary", ID: key, Boundary: "runner", At: user.Timestamp, Text: userText(user.Content)}}
			}
		case compactionNoteKind:
			note, err := durable.FromJsonValue[compactionNote](e.Data)
			if err != nil {
				break
			}
			rendered = []Item{{Kind: "boundary", ID: key, Boundary: "compaction", Status: note.Outcome, Reason: note.Error}}
		case "dex.withdrawn":
			m.Kind = inputKindForEntry(e.Kind, m)
			item := inputItem(m, key, 0, m.Content)
			rendered = []Item{item, {Kind: "error", ID: item.ID + ":error", Reason: "Stopped before the bot read it.", Aborted: true}}
		default:
			if len(e.Model) == 0 {
				break
			}
			switch msg := e.Model[0].(type) {
			case ai.UserMessage:
				if isDexInputEntry(e.Kind) || e.Kind == "pi.user" {
					m.Kind = inputKindForEntry(e.Kind, m)
					rendered = []Item{inputItem(m, key, msg.Timestamp, userText(msg.Content))}
				}
			case ai.AssistantMessage:
				rendered = p.assistantItems(msg, key, results, tools, p.live.Run != nil && (p.live.Generation == nil || p.live.Generation.Message == nil) && index == lastAssistant, true)
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
		items = append(items, p.assistantItems(*g.Message, fmt.Sprintf("partial:%d", g.Message.Timestamp), results, tools, true, false)...)
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
		rawKind, _ := q.Entry.Get("kind")
		kind, _ := rawKind.(string)
		m.Kind = inputKindForEntry(kind, m)
		item := inputItem(m, fmt.Sprint(q.Id), 0, m.Content)
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

func (p projection) assistantItems(m ai.AssistantMessage, key string, results map[string]ai.ToolResultMessage, tools map[string]harness.ToolSlot, live, committed bool) []Item {
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
			// The review page has validated data. Model-supplied settings arguments can contain secrets.
			if p.hideToolArguments == nil || !p.hideToolArguments(b.Name) {
				item.Args = b.Arguments
			}
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
var sessionCalls = []string{"session_new", "session_fork", "session_send"}

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
