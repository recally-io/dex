package app_test

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestUserReadsTelegramTableAsLabeledRows(t *testing.T) {
	t.Parallel()
	// Given a table with Chinese text, empty cells, and inline formatting.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	// When the model returns a table wider than a phone message bubble.
	f.message(1, 42, 42, "比较：\n\n| 方案 | 状态 | 说明 |  |\n| --- | --- | --- | --- |\n| [Dex](https://example.com/?a=1&b=2) | **就绪😀** | `x < y` & 长中文说明 | |\n| Beta | ~~等待~~ | [危险](javascript:alert) | 备注 |")
	// Then each row retains its header association, links, and safe inline HTML.
	got := f.next(t)
	for _, want := range []string{
		"1.\n<b>方案</b>: <a href=\"https://example.com/?a=1&amp;b=2\">Dex</a>",
		"<b>状态</b>: <b>就绪😀</b>",
		"<b>说明</b>: <code>x &lt; y</code> &amp; 长中文说明",
		"<b>Column 4</b>: —",
		"2.\n<b>方案</b>: Beta",
		"<b>状态</b>: <s>等待</s>",
		"<b>说明</b>: 危险",
		"<b>Column 4</b>: 备注",
	} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("missing labeled table content %q: %s", want, got.Text)
		}
	}
	if got.ParseMode != "HTML" || strings.Contains(got.Text, "<pre>") || strings.Contains(got.Text, "javascript:") {
		t.Fatalf("unreadable or unsafe table: %+v", got)
	}
	// Code in a header must not acquire an invalid surrounding bold entity.
	f.message(2, 42, 42, "Code header:\n\n| `key` |\n| --- |\n| value |")
	got = f.next(t)
	if !strings.Contains(got.Text, "1.\n<code>key</code>: value") {
		t.Fatalf("code header gained nested formatting: %+v", got)
	}
	// A header-only table still has content, even without any data rows.
	f.message(3, 42, 42, "Headers:\n\n| [文档](https://example.com) | |\n| --- | --- |")
	got = f.next(t)
	if !strings.Contains(got.Text, "<a href=\"https://example.com\">文档</a>\nColumn 2") {
		t.Fatalf("header-only table lost: %+v", got)
	}
	// Escaped URL attributes must not exceed the indivisible tag budget.
	f.message(4, 42, 42, "URL budget:\n\n| Link |\n| --- |\n| [large URL](https://example.com/?"+strings.Repeat("&", 980)+") |")
	got = f.next(t)
	if !strings.Contains(got.Text, "large URL") {
		got = f.next(t)
	}
	if len(utf16.Encode([]rune(got.Text))) > 4096 || !strings.Contains(got.Text, "<b>Link</b>: large URL") {
		t.Fatalf("oversized URL must keep readable text without a link: units=%d", len(utf16.Encode([]rune(got.Text))))
	}
}

func TestUserReceivesCompleteLongTelegramTable(t *testing.T) {
	t.Parallel()
	// Given table content that needs multiple messages, including one long code cell.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	cell := strings.Repeat("中文😀<&", 700)
	var table strings.Builder
	table.WriteString("Table:\n\n| Key | Value |\n| --- | --- |\n| long | `" + cell + "` |\n")
	for range 80 {
		table.WriteString("| row | 中文😀 & value |\n")
	}
	table.WriteString("| end | TABLE-END |")
	// When the model returns the complete table through the native engine and outbox.
	f.message(1, 42, 42, table.String())
	// Then all parts are valid HTML within the existing conservative UTF-16 limit.
	var text strings.Builder
	parts := 0
	for {
		got := f.next(t)
		parts++
		if got.ParseMode != "HTML" || len(utf16.Encode([]rune(got.Text))) > 4096 || !utf8.ValidString(got.Text) || strings.Contains(got.Text, "<table") || strings.Contains(got.Text, "<pre>") {
			t.Fatalf("invalid table part: %+v", got)
		}
		decoder := xml.NewDecoder(strings.NewReader("<root>" + got.Text + "</root>"))
		for {
			token, err := decoder.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("unbalanced HTML: %v: %s", err, got.Text)
			}
			if data, ok := token.(xml.CharData); ok {
				text.Write(data)
			}
		}
		if strings.Contains(got.Text, "TABLE-END") {
			break
		}
	}
	if parts < 2 || !strings.Contains(text.String(), cell) || strings.Count(text.String(), "中文😀 & value") != 80 || !strings.Contains(text.String(), "82.\nKey: end\nValue: TABLE-END") {
		t.Fatalf("table content lost: parts=%d text length=%d", parts, len(text.String()))
	}
}
