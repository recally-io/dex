package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestUserSeesOnlyCurrentChatText(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("字", 24_000)
	stream := ": keepalive\r\n\r\n" +
		`event: run
data: {"type":"run","running":true}

event: snapshot
data: {"type":"snapshot",
data: "items":[{"kind":"user","id":"u","text":"hi"},{"kind":"thinking","id":"a","text":"hidden"},{"kind":"assistant","id":"b","text":"later"}]}

event: item
data: {"type":"item","item":{"kind":"assistant","id":"a","text":"yes"}}

event: append
data: {"type":"append","id":"a","text":"!"}

event: item
data: {"type":"item","item":{"kind":"thinking","id":"a","text":"hidden again"}}

event: append
data: {"type":"append","id":"a","text":"still hidden"}

` + fmt.Sprintf("event: snapshot\r\ndata: {\"type\":\"snapshot\",\"items\":[{\"kind\":\"assistant\",\"id\":\"new\",\"text\":%q}]}\r\n\r\n", long) +
		"event: append\ndata: {\"type\":\"append\",\"id\":\"new\",\"text\":\"incomplete\"}\n"

	var updates [][]chatLine
	err := readChat(t.Context(), strings.NewReader(stream), func(lines []chatLine) {
		updates = append(updates, lines)
	})

	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v, want EOF", err)
	}
	u := chatLine{"u", "user", "hi"}
	b := chatLine{"b", "assistant", "later"}
	want := [][]chatLine{
		{u, b},
		{u, {"a", "assistant", "yes"}, b},
		{u, {"a", "assistant", "yes!"}, b},
		{u, b},
		{{"new", "assistant", long}},
	}
	if !slices.EqualFunc(updates, want, slices.Equal[[]chatLine]) {
		t.Fatalf("got %d updates, want %d with the expected text and order", len(updates), len(want))
	}
	updates[0][0].Text = "changed by the caller"
	if updates[1][0] != u {
		t.Fatal("updates share storage")
	}
}

func TestUserDoesNotSeeReadyWithoutSnapshot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, stream, message string
		cause                 error
	}{
		{name: "EOF", stream: ": ping\n\n", cause: io.ErrUnexpectedEOF},
		{name: "out of order", stream: "event: append\ndata: {}\n\n", message: "did not start with a snapshot"},
		{name: "invalid JSON", stream: "event: snapshot\ndata: {\n\n", message: "decode snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := readChat(t.Context(), strings.NewReader(tc.stream), func([]chatLine) { t.Fatal("unexpected update") })
			if err == nil || !strings.Contains(err.Error(), tc.message) || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("unexpected stream error: %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := readChat(ctx, strings.NewReader(""), func([]chatLine) { t.Fatal("unexpected update") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
}
