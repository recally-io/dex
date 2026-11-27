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
	err := readChat(t.Context(), strings.NewReader(stream), func(update chatUpdate) {
		updates = append(updates, update.Lines)
	})

	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v, want EOF", err)
	}
	u := chatLine{ID: "u", Role: "user", Text: "hi"}
	b := chatLine{ID: "b", Role: "assistant", Text: "later"}
	want := [][]chatLine{
		{u, b},
		{u, {ID: "a", Role: "assistant", Text: "yes"}, b},
		{u, {ID: "a", Role: "assistant", Text: "yes!"}, b},
		{u, b},
		{{ID: "new", Role: "assistant", Text: long}},
	}
	if !slices.EqualFunc(updates, want, slices.Equal[[]chatLine]) {
		t.Fatalf("got %d updates, want %d with the expected text and order", len(updates), len(want))
	}
	updates[0][0].Text = "changed by the caller"
	if updates[1][0] != u {
		t.Fatal("updates share storage")
	}
}

func TestUserSeesOnlyCurrentRunAndToolOutput(t *testing.T) {
	t.Parallel()

	// Given a running snapshot, a successor run, and a replacement snapshot.
	stream := `event: snapshot
data: {"type":"snapshot","items":[],"running":true,"runTarget":"first"}

event: run
data: {"type":"run","running":true,"runTarget":"second"}

event: item
data: {"type":"item","item":{"kind":"tool","id":"call","name":"echo","status":"done","output":"中文工具结果"}}

event: snapshot
data: {"type":"snapshot","items":[],"running":false}

`
	// When the client reads the stream, it receives only the current run target.
	var got []string
	var tool string
	err := readChat(t.Context(), strings.NewReader(stream), func(update chatUpdate) {
		got = append(got, update.RunTarget)
		if len(update.Lines) == 1 {
			tool = update.Lines[0].Text
		}
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v, want EOF", err)
	}

	// Then the tool result is visible and the new snapshot clears the target.
	if want := []string{"first", "second", "second", ""}; !slices.Equal(got, want) {
		t.Fatalf("run targets = %q, want %q", got, want)
	}
	if !strings.Contains(tool, "中文工具结果") {
		t.Fatalf("tool text = %q, want the tool result", tool)
	}
}

func TestUserSeesAttachmentAndResetChangesCommandAvailability(t *testing.T) {
	t.Parallel()

	stream := `event: snapshot
data: {"type":"snapshot","items":[{"kind":"user","id":"file","text":"","attachments":[{"id":"upload-id","name":"报告.txt"}]}]}

event: item
data: {"type":"item","item":{"kind":"boundary","id":"reset","boundary":"reset"}}

event: item
data: {"type":"item","item":{"kind":"boundary","id":"summary","boundary":"compaction","text":"旧摘要"}}

event: item
data: {"type":"item","item":{"kind":"boundary","id":"reset","boundary":"reset","text":"交接"}}

`
	var availability []bool
	var file chatLine
	err := readChat(t.Context(), strings.NewReader(stream), func(update chatUpdate) {
		availability = append(availability, update.HasContext)
		if len(update.Lines) > 0 {
			file = update.Lines[0]
		}
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v, want EOF", err)
	}
	if file.Attachments != "报告.txt" || file.Text != "" {
		t.Fatalf("attachment-only message = %+v", file)
	}
	if want := []bool{true, false, true}; !slices.Equal(availability, want) {
		t.Fatalf("command availability = %v, want %v", availability, want)
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
			err := readChat(t.Context(), strings.NewReader(tc.stream), func(chatUpdate) { t.Fatal("unexpected update") })
			if err == nil || !strings.Contains(err.Error(), tc.message) || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("unexpected stream error: %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := readChat(ctx, strings.NewReader(""), func(chatUpdate) { t.Fatal("unexpected update") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
}
