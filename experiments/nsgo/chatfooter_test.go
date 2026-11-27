package main

import (
	"testing"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestUserCopiesCommittedMessagesWithTranscriptFooters(t *testing.T) {
	t.Parallel()
	const user = "Question\n第二行"
	const answer = "**Answer**\n中文"

	// Given a completed turn on a touch screen, with metadata from Dex.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return core.View{Children: []core.Node{
			chatUserFooter(cx, func() chatLine { return chatLine{Role: "user", Text: user, EntryID: 5} }, nil),
			chatAssistantFooter(cx, func() chatLine {
				return chatLine{Role: "assistant", Text: answer, Model: "faux/echo", Ending: true,
					HasUsage: true, Usage: chatUsage{Output: 1234}}
			}, nil),
		}}
	}, nsgotest.Idiom(core.IdiomPhone))
	app.Find("text:faux/echo")
	app.Find("text:0 in · 1,234 out")

	// When the user copies either message, the clipboard keeps the exact source.
	app.Press("id:chat-copy-message")
	if got := app.Clipboard; got != user {
		t.Fatalf("copied user message = %q, want %q", got, user)
	}
	app.Press("id:chat-copy-answer")
	if got := app.Clipboard; got != answer {
		t.Fatalf("copied answer = %q, want %q", got, answer)
	}
}
