package main

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

var chatGateway = "http://127.0.0.1:19487"

var chatClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type chatLine struct{ ID, Role, Text string }

type chatUpdate struct {
	Lines     []chatLine
	Running   bool
	RunTarget string
}

type chatSession struct {
	ID         int    `json:"id"`
	Title      string `json:"title"`
	Channel    string `json:"channel"`
	Main       bool   `json:"main"`
	ReadOnly   *bool  `json:"readOnly"`
	ArchivedAt int64  `json:"archivedAt"`
	Busy       bool   `json:"busy"`
	BotID      string `json:"botId"`
	BotName    string `json:"botName"`
}

func (s chatSession) writable() bool { return s.ReadOnly != nil && !*s.ReadOnly }

func chatSessionsPath(botID string) string { return botPath(botID) + "/sessions" }

func chatSessionPath(botID string, id int) string {
	if id == 0 {
		return chatSessionsPath(botID) + "/main"
	}
	return chatSessionsPath(botID) + "/" + strconv.Itoa(id)
}

func chatGetSession(ctx context.Context, botID string, id int) (chatSession, error) {
	var session chatSession
	err := requestJSON(ctx, http.MethodGet, chatSessionPath(botID, id), nil, &session)
	if err == nil && (session.ID <= 0 || (id > 0 && id != session.ID)) {
		err = errors.New("会话 ID 与请求不符")
	}
	return session, err
}

func watchChat(ctx context.Context, botID string, id int, update func(chatUpdate)) error {
	res, err := gatewayRequest(ctx, http.MethodGet, chatSessionPath(botID, id)+"/events", "text/event-stream", nil, "")
	if err != nil {
		return err
	}
	defer closeChatBody(ctx, res.Body)()
	return readChat(ctx, res.Body, update)
}

func sendChat(ctx context.Context, botID string, id int, requestID, text string) error {
	return requestJSON(ctx, http.MethodPost, chatSessionPath(botID, id)+"/messages", struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}{requestID, text}, nil)
}

func chatStopRun(ctx context.Context, botID string, id int, target string) error {
	path := chatSessionPath(botID, id) + "/abort"
	if target != "" {
		path += "?" + url.Values{"runTarget": {target}}.Encode()
	}
	return requestJSON(ctx, http.MethodPost, path, nil, nil)
}

func closeChatBody(ctx context.Context, body io.ReadCloser) func() {
	closeBody := func() { _ = body.Close() }
	if runtime.GOOS != "js" {
		return closeBody
	}
	// The Fetch transport stops watching the request context at the headers.
	closeOnce := sync.OnceFunc(closeBody)
	stop := context.AfterFunc(ctx, closeOnce)
	return func() {
		stop()
		closeOnce()
	}
}

type chatItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Output    string `json:"output"`
	Reason    string `json:"reason"`
	Queued    bool   `json:"queued"`
	Truncated bool   `json:"truncated"`
}

func (item chatItem) visible() bool {
	switch item.Kind {
	case "user", "assistant", "tool", "error", "session_result", "wake", "task_wake":
		return true
	default:
		return false
	}
}

func (item chatItem) line() chatLine {
	line := chatLine{item.ID, item.Kind, item.Text}
	switch item.Kind {
	case "user":
		if item.Queued {
			line.Role = "queued"
		}
	case "tool":
		status := map[string]string{"running": "运行中", "done": "完成", "error": "失败"}[item.Status]
		line.Text = item.Name + " · " + status
		if item.Output != "" {
			line.Text += "\n" + item.Output
		}
		if item.Truncated {
			line.Text += "\n输出过长，当前快照未包含完整内容。"
		}
	case "error":
		line.Text = item.Reason
	case "session_result", "wake", "task_wake":
		line.Text = item.Title
		if item.Reason != "" {
			line.Text += "\n" + item.Reason
		}
	}
	return line
}

func readChat(ctx context.Context, stream io.Reader, update func(chatUpdate)) error {
	reader := bufio.NewReader(stream)
	var items []chatItem
	var state chatUpdate
	ready := false
	for ctx.Err() == nil {
		kind, data, err := readChatEvent(reader)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			if !ready && errors.Is(err, io.EOF) {
				return fmt.Errorf("stream ended before the first snapshot: %w", io.ErrUnexpectedEOF)
			}
			return err
		}
		switch kind {
		case "snapshot", "item", "append":
		case "run":
			if !ready {
				continue
			}
		default:
			continue
		}
		if !ready && kind != "snapshot" {
			return errors.New("stream did not start with a snapshot")
		}

		var event struct {
			Type      string     `json:"type"`
			Items     []chatItem `json:"items"`
			Item      chatItem   `json:"item"`
			ID        string     `json:"id"`
			Text      string     `json:"text"`
			Running   bool       `json:"running"`
			RunTarget string     `json:"runTarget"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode %s event: %w", kind, err)
		}
		if event.Type != kind {
			return fmt.Errorf("%s event has type %q", kind, event.Type)
		}
		switch kind {
		case "snapshot":
			if event.Items == nil {
				return errors.New("snapshot has no items array")
			}
			items, ready = event.Items, true
			state.Running, state.RunTarget = event.Running, event.RunTarget
		case "run":
			state.Running, state.RunTarget = event.Running, event.RunTarget
		case "item":
			if event.Item.ID == "" {
				return errors.New("item event has no item ID")
			}
			i := slices.IndexFunc(items, func(item chatItem) bool { return item.ID == event.Item.ID })
			visible := event.Item.visible()
			// Keep hidden items in order because a later event can change their kind.
			if i < 0 {
				items = append(items, event.Item)
			} else {
				visible = visible || items[i].visible()
				items[i] = event.Item
			}
			if !visible {
				continue
			}
		case "append":
			if event.ID == "" {
				return errors.New("append event has no item ID")
			}
			i := slices.IndexFunc(items, func(item chatItem) bool { return item.ID == event.ID })
			if i < 0 || !items[i].visible() {
				continue
			}
			items[i].Text += event.Text
		}

		lines := make([]chatLine, 0, len(items))
		for _, item := range items {
			if item.visible() {
				lines = append(lines, item.line())
			}
		}
		state.Lines = lines
		update(state)
	}
	return ctx.Err()
}

func readChatEvent(reader *bufio.Reader) (string, []byte, error) {
	var kind string
	var data bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// A blank line must end the event, even at EOF.
			return "", nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if data.Len() != 0 {
				return kind, data.Bytes(), nil
			}
			kind = ""
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			kind = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
}
