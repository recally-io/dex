package app_test

import (
	"encoding/json/v2"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// useRichAnswerProvider sends large model answers in one local SSE delta. The
// native engine and SQLite still run; only the external provider is a fixture.
func useRichAnswerProvider(t *testing.T, d *dex) func(string) {
	t.Helper()
	var answer atomic.Pointer[string]
	answer.Store(new(""))
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		delta, _ := json.Marshal(map[string]any{"id": "rich", "object": "chat.completion.chunk", "created": 1, "model": "rich", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": *answer.Load()}, "finish_reason": nil}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: {\"id\":\"rich\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"rich\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", delta)
	}))
	t.Cleanup(endpoint.Close)
	d.must(t, http.MethodPut, "/api/settings/providers/rich-fixture", map[string]any{"api": "openai-completions", "baseUrl": endpoint.URL, "models": []string{"rich"}}, nil)
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Dex", "provider": "rich-fixture", "modelId": "rich", "thinkingLevel": "off"}, nil)
	return func(text string) { answer.Store(new(text)) }
}

// richPayloadText checks the real HTTP payload, not a browser approximation.
func richPayloadText(t *testing.T, message sentMessage) string {
	t.Helper()
	if message.RichHTML == "" || !utf8.ValidString(message.RichHTML) {
		t.Fatalf("not a valid rich payload: %+v", message)
	}
	decoder := xml.NewDecoder(strings.NewReader("<root>" + message.RichHTML + "</root>"))
	var plain strings.Builder
	depth, blocks := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("invalid rich HTML: %v", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Local != "root" {
				depth++
			}
			if depth > 16 {
				t.Fatal("rich nesting exceeded 16")
			}
			switch token.Name.Local {
			case "table", "tr", "blockquote", "pre", "p", "hr":
				blocks++
			}
		case xml.EndElement:
			if token.Name.Local != "root" {
				depth--
			}
		case xml.CharData:
			plain.Write(token)
		}
	}
	if len([]rune(plain.String())) > 32768 || blocks > 500 {
		t.Fatalf("rich limits: characters=%d blocks=%d", len([]rune(plain.String())), blocks)
	}
	return plain.String()
}

func TestUserReceivesCompleteSplitRichTelegramTable(t *testing.T) {
	t.Parallel()
	// Given more table rows than one rich message permits.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	setAnswer := useRichAnswerProvider(t, d)
	var table strings.Builder
	table.WriteString("Table:\n\n| Key | Value |\n| --- | --- |\n")
	for range 600 {
		table.WriteString("| row | 中文😀 & value |\n")
	}
	table.WriteString("| end | TABLE-END |")
	// When the model returns the table through native execution and the outbox.
	setAnswer(table.String())
	f.message(1, 42, 42, "generate table")
	// Then every message has a complete native table and repeated headers, without lost rows.
	var text strings.Builder
	parts := 0
	for {
		got := f.next(t)
		parts++
		text.WriteString(richPayloadText(t, got))
		if !strings.Contains(got.RichHTML, "<th>Key</th><th>Value</th>") {
			t.Fatalf("missing repeated table header: %s", got.RichHTML)
		}
		if strings.Contains(got.RichHTML, "TABLE-END") {
			break
		}
	}
	if parts < 2 || strings.Count(text.String(), "中文😀 & value") != 600 {
		t.Fatalf("table lost rows: parts=%d rows=%d", parts, strings.Count(text.String(), "中文😀 & value"))
	}
}

func TestUserKeepsRichContentBeyondTableLimits(t *testing.T) {
	t.Parallel()
	// Given a cell larger than the rich text budget and a table wider than 20 columns.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	setAnswer := useRichAnswerProvider(t, d)
	cell := strings.Repeat("中文😀<&", 7000)
	// When a whole native row cannot fit, the same rich renderer uses labeled paragraphs.
	setAnswer("Large cell:\n\n| Key | Value |\n| --- | --- |\n| long | `" + cell + "` |\n| end | CELL-END |")
	f.message(1, 42, 42, "generate cell")
	var text strings.Builder
	for {
		got := f.next(t)
		text.WriteString(richPayloadText(t, got))
		if strings.Contains(got.RichHTML, "CELL-END") {
			break
		}
	}
	if !strings.Contains(text.String(), cell) {
		t.Fatal("long cell was truncated or changed")
	}
	var table strings.Builder
	for i := range 21 {
		table.WriteString("| H" + strconv.Itoa(i) + " ")
	}
	table.WriteString("|\n")
	for range 21 {
		table.WriteString("| --- ")
	}
	table.WriteString("|\n")
	for i := range 21 {
		table.WriteString("| V" + strconv.Itoa(i) + " ")
	}
	table.WriteString("|")
	setAnswer("Wide table:\n\n" + table.String())
	f.message(2, 42, 42, "generate wide table")
	got := f.next(t)
	plain := richPayloadText(t, got)
	// Then content stays in Rich Messages, with each header associated with its value.
	if strings.Contains(got.RichHTML, "<table") {
		t.Fatal("sent a native table above 20 columns")
	}
	for i := range 21 {
		if !strings.Contains(plain, "H"+strconv.Itoa(i)+": V"+strconv.Itoa(i)) {
			t.Fatalf("lost column %d: %s", i, plain)
		}
	}
}

func TestUserGetsNativeRichTelegramTextAndTable(t *testing.T) {
	t.Parallel()
	// Given a real native engine and outbox connected to a local Telegram HTTP server.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	// When the model answers with plain text and then a formatted table.
	f.message(1, 42, 42, "hello")
	plain := f.next(t)
	if plain.RichHTML != "<p>echo: hello</p>" {
		t.Fatalf("new plain text did not use Rich Messages: %+v", plain)
	}
	f.message(2, 42, 42, "比较：\n\n| 方案 | 状态 | `key` | |\n| --- | --- | --- | --- |\n| [Dex](https://example.com/?a=1&b=2) | **就绪😀** | `x < y` & 长中文说明 | |\n| Beta | ~~等待~~ | [危险](javascript:alert) | 备注 |")
	table := f.next(t)
	// Then both messages use the same native rich payload, with a real table.
	for _, want := range []string{"<table", "<th>方案</th>", `<a href="https://example.com/?a=1&amp;b=2">Dex</a>`, "<b>就绪😀</b>", "<th><code>key</code></th><th></th>", "<code>x &lt; y</code> &amp; 长中文说明", "<s>等待</s>", "<td>危险</td><td>备注</td>"} {
		if !strings.Contains(table.RichHTML, want) {
			t.Fatalf("missing native table content %q: %+v", want, table)
		}
	}
	if strings.Contains(table.RichHTML, "javascript:") {
		t.Fatal("unsafe link became active")
	}
	richPayloadText(t, table)
	f.message(3, 42, 42, "Headers:\n\n| [文档](https://example.com) | |\n| --- | --- |")
	header := f.next(t)
	if !strings.Contains(header.RichHTML, `<th><a href="https://example.com">文档</a></th><th></th>`) {
		t.Fatalf("header-only table lost: %+v", header)
	}
	// The URL policy counts escaped attribute bytes, independently of the rich
	// decoded-character budget. A rejected URL keeps the complete link label.
	f.message(4, 42, 42, "URL budget:\n\n| Link |\n| --- |\n| [large URL](https://example.com/?"+strings.Repeat("&", 980)+") |")
	url := f.next(t)
	if !strings.Contains(url.RichHTML, "<td>large URL</td>") || strings.Contains(url.RichHTML, "href=") {
		t.Fatalf("escaped URL policy changed: %+v", url)
	}
	richPayloadText(t, url)
}
