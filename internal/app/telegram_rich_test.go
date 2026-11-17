package app_test

import (
	"strings"
	"testing"
)

func TestUserGetsNativeRichTelegramTextAndTable(t *testing.T) {
	t.Parallel()
	// Given a real native engine and outbox connected to a local Telegram HTTP server.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	// When the model answers with plain text and then a formatted table.
	f.message(1, 42, 42, "hello")
	plain := f.next(t)
	if plain.RichHTML != "<p>echo: hello</p>" || plain.ParseMode != "" {
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
