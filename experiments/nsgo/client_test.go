package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUserSeesOnlyCurrentChatText(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("字", 24_000)
	stream := ": keepalive\r\n\r\n" +
		`event: run
data: {"type":"run","running":true}

event: snapshot
data: {"type":"snapshot",
data: "items":[{"kind":"user","id":"u","text":"hi","at":1792024200123},{"kind":"thinking","id":"a","text":"hidden"},{"kind":"assistant","id":"b","text":"later"}]}

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
	u := chatLine{ID: "u", Role: "user", Text: "hi", At: 1792024200123}
	b := chatLine{ID: "b", Role: "assistant", Text: "later", Ending: true}
	want := [][]chatLine{
		{u, b},
		{u, {ID: "a", Role: "assistant", Text: "yes"}, b},
		{u, {ID: "a", Role: "assistant", Text: "yes!"}, b},
		{u, b},
		{{ID: "new", Role: "assistant", Text: long, Ending: true}},
	}
	if !slices.EqualFunc(updates, want, slices.Equal[[]chatLine]) {
		t.Fatalf("got %d updates, want %d with the expected text and order", len(updates), len(want))
	}
	updates[0][0].Text = "changed by the caller"
	if updates[1][0] != u {
		t.Fatal("updates share storage")
	}
}

func TestUserSeesCommittedTranscriptMetadata(t *testing.T) {
	t.Parallel()

	// Given a user entry, a streamed answer, and its final metadata from Dex.
	stream := `event: snapshot
data: {"type":"snapshot","running":true,"items":[{"kind":"user","id":"u","entryId":5,"origin":"telegram:7","at":1792024200123,"text":"Question"},{"kind":"assistant","id":"draft","model":"faux/echo","text":"Partial"}]}

event: append
data: {"type":"append","id":"draft","text":" answer"}

event: item
data: {"type":"item","item":{"kind":"assistant","id":"draft","entryId":7,"model":"faux/echo","usage":{"input":0,"output":1234},"text":"Complete answer"}}

event: run
data: {"type":"run","running":false}

event: item
data: {"type":"item","item":{"kind":"user","id":"queued","queued":true,"text":"Next question"}}

`
	var updates []chatUpdate
	err := readChat(t.Context(), strings.NewReader(stream), func(update chatUpdate) { updates = append(updates, update) })
	if !errors.Is(err, io.EOF) || len(updates) != 5 {
		t.Fatalf("got %d updates ending in %v, want five updates and EOF", len(updates), err)
	}
	if user := updates[0].Lines[0]; user.EntryID != 5 || user.Origin != "telegram:7" || user.At != 1792024200123 {
		t.Fatalf("user entry metadata = %+v", user)
	}
	if answer := updates[1].Lines[1]; !answer.Live || !answer.Ending || answer.HasUsage || answer.Text != "Partial answer" {
		t.Fatalf("streamed answer = %+v", answer)
	}
	if answer := updates[3].Lines[1]; answer.Live || !answer.Ending || answer.EntryID != 7 || answer.Model != "faux/echo" || !answer.HasUsage || answer.Usage != (chatUsage{Input: 0, Output: 1234}) {
		t.Fatalf("committed answer = %+v", answer)
	}
	if answer, next := updates[4].Lines[1], updates[4].Lines[2]; !answer.Ending || next.Role != "queued" || next.EntryID != 0 {
		t.Fatalf("turn followed by queued input = %+v / %+v", answer, next)
	}
}

func TestUserSeesLocalMessageTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.FixedZone("UTC+08", 8*60*60))
	for _, tc := range []struct {
		utc, want string
	}{
		{"", ""},
		{"2026-10-15T16:04:00Z", "00:04"},
		{"2026-10-15T00:30:00Z", "Thu 15 Oct, 08:30"},
		{"2026-09-03T00:30:00Z", "Thu 3 Sept, 08:30"},
		{"2025-10-05T16:04:00Z", "Mon 6 Oct, 00:04"},
	} {
		var ms int64
		if tc.utc != "" {
			at, err := time.Parse(time.RFC3339, tc.utc)
			if err != nil {
				t.Fatal(err)
			}
			ms = at.UnixMilli()
		}
		if got := chatStampAt(ms, now); got != tc.want {
			t.Errorf("stamp for %q = %q, want %q", tc.utc, got, tc.want)
		}
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
