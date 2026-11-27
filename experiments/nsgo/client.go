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
	"runtime"
	"slices"
	"strings"
	"sync"
)

const chatGateway = "http://127.0.0.1:19487"
const chatSessions = "/api/bots/default/sessions/"

var chatClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type chatLine struct{ ID, Role, Text string }

func openMain(ctx context.Context) (int, error) {
	res, err := chatRequest(ctx, http.MethodGet, chatSessions+"main", "application/json", nil, http.StatusOK)
	if err != nil {
		return 0, err
	}
	defer closeChatBody(ctx, res.Body)()

	var session struct {
		ID int `json:"id"`
	}
	if err := json.UnmarshalRead(res.Body, &session); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("decode main session: %w", err)
	}
	if session.ID <= 0 {
		return 0, errors.New("main session has no valid ID")
	}
	return session.ID, nil
}

func watchChat(ctx context.Context, id int, update func([]chatLine)) error {
	path := fmt.Sprintf("%s%d/events", chatSessions, id)
	res, err := chatRequest(ctx, http.MethodGet, path, "text/event-stream", nil, http.StatusOK)
	if err != nil {
		return err
	}
	defer closeChatBody(ctx, res.Body)()
	return readChat(ctx, res.Body, update)
}

func sendChat(ctx context.Context, id int, requestID, text string) error {
	body, err := json.Marshal(struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}{requestID, text})
	if err != nil {
		return fmt.Errorf("encode chat message: %w", err)
	}
	path := fmt.Sprintf("%s%d/messages", chatSessions, id)
	res, err := chatRequest(ctx, http.MethodPost, path, "application/json", body, http.StatusAccepted)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}

func chatRequest(ctx context.Context, method, path, accept string, body []byte, status int) (*http.Response, error) {
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, chatGateway+path, input)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if runtime.GOOS == "js" {
		// Fetch can follow a redirect before net/http sees the response.
		req.Header.Set("js.fetch:redirect", "error")
	}
	res, err := chatClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != status {
		_ = res.Body.Close()
		return nil, fmt.Errorf("%s %s: HTTP %s", method, path, res.Status)
	}
	return res, nil
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
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (item chatItem) visible() bool {
	return item.Kind == "user" || item.Kind == "assistant"
}

func readChat(ctx context.Context, stream io.Reader, update func([]chatLine)) error {
	reader := bufio.NewReader(stream)
	var items []chatItem
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
		default:
			continue
		}
		if !ready && kind != "snapshot" {
			return errors.New("stream did not start with a snapshot")
		}

		var event struct {
			Type  string     `json:"type"`
			Items []chatItem `json:"items"`
			Item  chatItem   `json:"item"`
			ID    string     `json:"id"`
			Text  string     `json:"text"`
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
				lines = append(lines, chatLine{ID: item.ID, Role: item.Kind, Text: item.Text})
			}
		}
		update(lines)
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
