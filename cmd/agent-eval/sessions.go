//go:build agenteval

package main

import (
	"bufio"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/engine"
)

func (c client) history(path string) ([]engine.Item, error) {
	before := int64(9223372036854775807)
	var items []engine.Item
	for {
		var page engine.History
		if err := c.request("GET", path+"/history?limit=200&before="+strconv.FormatInt(before, 10), nil, &page); err != nil {
			return items, err
		}
		items = append(page.Items, items...)
		if page.NextBefore == 0 {
			return items, nil
		}
		before = page.NextBefore
	}
}

func (c client) poll(what string, f func() (bool, error)) error {
	for {
		ok, err := f()
		if err != nil || ok {
			return err
		}
		select {
		case <-c.ctx.Done():
			return fmt.Errorf("wait for %s: %w", what, c.ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (c client) send(path, text, mode string) error {
	return c.request("POST", path+"/messages", map[string]any{"id": uuid.New().String(), "text": text, "mode": mode}, nil)
}

// A text entry with a tool call is not a final answer, even if it has text.
func answers(items []engine.Item) []engine.Item {
	toolEntries := map[int64]bool{}
	for _, item := range items {
		if item.Kind == "tool" {
			toolEntries[item.EntryID] = true
		}
	}
	var out []engine.Item
	for _, item := range items {
		if item.Kind == "assistant" && item.EntryID != 0 && !toolEntries[item.EntryID] && strings.TrimSpace(item.Text) != "" {
			out = append(out, item)
		}
	}
	return out
}

func inputIndex(items []engine.Item, text string) int {
	for i, item := range items {
		if item.Kind == "user" && item.Text == text && item.EntryID != 0 {
			return i
		}
	}
	return -1
}

func answerIndex(items []engine.Item, answer engine.Item) int {
	for i, item := range items {
		if item.ID == answer.ID {
			return i
		}
	}
	return -1
}

func (c client) waitForSlowTool(path string) error {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, c.base+path+"/events", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events: HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event engine.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return err
		}
		items := event.Items
		if event.Item != nil {
			items = append(items, *event.Item)
		}
		for _, item := range items {
			args, err := json.Marshal(item.Args)
			if err != nil {
				return err
			}
			if item.Kind == "tool" && item.Name == "bash" && item.Status == "running" && strings.Contains(string(args), "sleep 20") {
				return nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("event stream ended before slow bash started")
}

func (c client) sessionOperation(path string, id int64, op string) ([]int64, []check, error) {
	ids := []int64{id}
	var checks []check
	add := func(name string, pass bool, detail string) {
		checks = append(checks, check{Name: name, Pass: pass, Detail: detail})
	}
	wait := func() error { _, _, err := c.waitForWork(id, false); return err }
	switch op {
	case "steer", "followUp":
		if err := c.waitForSlowTool(path); err != nil {
			return ids, checks, err
		}
		var busy chat
		if err := c.request("GET", path, nil, &busy); err != nil {
			return ids, checks, err
		}
		text := "Also include the word BANANA in your final answer."
		if op == "followUp" {
			text = "Reply with exactly: SECOND"
		}
		if err := c.send(path, text, op); err != nil {
			return ids, checks, err
		}
		add("input accepted while busy", busy.Busy, "")
		if err := wait(); err != nil {
			return ids, checks, err
		}
		items, err := c.history(path)
		if err != nil {
			return ids, checks, err
		}
		final := answers(items)
		idx := inputIndex(items, text)
		tool := false
		for _, item := range items {
			if item.Kind == "tool" && item.Name == "bash" && strings.Contains(item.Output, "first-step") {
				tool = true
			}
		}
		add("slow bash completed", tool, "")
		if op == "steer" {
			add("one final answer", len(final) == 1, strconv.Itoa(len(final)))
			add("answer contains BANANA", len(final) == 1 && strings.Contains(strings.ToUpper(final[0].Text), "BANANA"), "")
			add("steer placed before final answer in same run", len(final) == 1 && idx >= 0 && idx < answerIndex(items, final[0]), "")
		} else {
			add("two final answers", len(final) == 2, strconv.Itoa(len(final)))
			add("second answer contains SECOND", len(final) == 2 && strings.Contains(strings.ToUpper(final[1].Text), "SECOND"), "")
			add("follow-up placed after first answer", len(final) == 2 && idx > answerIndex(items, final[0]) && idx < answerIndex(items, final[1]), "")
		}
	case "fork":
		if err := wait(); err != nil {
			return ids, checks, err
		}
		before, err := c.history(path)
		if err != nil {
			return ids, checks, err
		}
		final := answers(before)
		if len(final) == 0 {
			return ids, checks, errors.New("source has no final answer")
		}
		entry := final[len(final)-1].EntryID
		request := map[string]any{"id": uuid.New().String(), "entryId": entry}
		var fork, replay struct {
			ChatID int64 `json:"chatId"`
		}
		if err := c.request("POST", path+"/fork", request, &fork); err != nil {
			return ids, checks, err
		}
		ids = append(ids, fork.ChatID)
		if err := c.request("POST", path+"/fork", request, &replay); err != nil {
			return ids, checks, err
		}
		add("fork replay returns same chat", fork.ChatID != 0 && fork.ChatID == replay.ChatID, "")
		otherEntry := int64(0)
		for _, item := range before {
			if item.Kind == "user" && item.EntryID != 0 && item.EntryID != entry {
				otherEntry = item.EntryID
				break
			}
		}
		if otherEntry == 0 {
			return ids, checks, errors.New("source has no other entry")
		}
		request["entryId"] = otherEntry
		err = c.request("POST", path+"/fork", request, nil)
		status, ok := errors.AsType[*httpError](err)
		add("fork replay with another entry conflicts", ok && status.status == http.StatusConflict, fmt.Sprint(err))
		forkPath := "/api/agents/default/chats/" + strconv.FormatInt(fork.ChatID, 10)
		if err := c.send(forkPath, "What is the code word?", "followUp"); err != nil {
			return ids, checks, err
		}
		if _, _, err := c.waitForWork(fork.ChatID, false); err != nil {
			return ids, checks, err
		}
		forkItems, err := c.history(forkPath)
		if err != nil {
			return ids, checks, err
		}
		forkAnswers := answers(forkItems)
		ask := inputIndex(forkItems, "What is the code word?")
		add("fork answer contains KIWI", len(forkAnswers) > 0 && ask >= 0 && answerIndex(forkItems, forkAnswers[len(forkAnswers)-1]) > ask && strings.Contains(strings.ToUpper(forkAnswers[len(forkAnswers)-1].Text), "KIWI"), "")
		after, err := c.history(path)
		if err != nil {
			return ids, checks, err
		}
		add("source history count unchanged", len(after) == len(before), fmt.Sprintf("%d -> %d", len(before), len(after)))
	case "compact":
		if err := wait(); err != nil {
			return ids, checks, err
		}
		request := map[string]any{"id": uuid.New().String()}
		if err := c.request("POST", path+"/compact", request, nil); err != nil {
			return ids, checks, err
		}
		var items []engine.Item
		err := c.poll("compaction outcome", func() (bool, error) {
			var err error
			items, err = c.history(path)
			if err != nil {
				return false, err
			}
			for _, item := range items {
				if item.Kind == "boundary" && item.Boundary == "compaction" && item.Status != "pending" && item.Status != "queued" {
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			return ids, checks, err
		}
		count, success := compactions(items)
		add("compaction finished", count == 1 && success, strconv.Itoa(count))
		if err := c.request("POST", path+"/compact", request, nil); err != nil {
			return ids, checks, err
		}
		if err := c.send(path, "What is my favorite color?", "followUp"); err != nil {
			return ids, checks, err
		}
		if err := wait(); err != nil {
			return ids, checks, err
		}
		after, err := c.history(path)
		if err != nil {
			return ids, checks, err
		}
		n, _ := compactions(after)
		add("compact replay adds no entry", n == count, fmt.Sprintf("%d -> %d", count, n))
		final := answers(after)
		ask := inputIndex(after, "What is my favorite color?")
		add("answer contains TEAL", len(final) > 0 && ask >= 0 && answerIndex(after, final[len(final)-1]) > ask && strings.Contains(strings.ToUpper(final[len(final)-1].Text), "TEAL"), "")
	default:
		return ids, checks, fmt.Errorf("unknown session operation %q", op)
	}
	return ids, checks, nil
}

func compactions(items []engine.Item) (int, bool) {
	count, success := 0, true
	for _, item := range items {
		if item.Kind == "boundary" && item.Boundary == "compaction" {
			count++
			success = success && (item.Status == "nothing" || item.Status == "" && item.Text != "")
		}
	}
	return count, success
}
