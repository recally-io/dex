package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func compactionExtension() *durable.Extension {
	return &durable.Extension{Name: "local-compaction", Hooks: []durable.HookRegistration{
		harness.Hook(harness.CompactionTask, &harness.CompactionHooks{BeforeCompact: localCompaction}),
	}}
}

// localCompaction projects a short view of the history that PiG plans to replace.
// PiG owns the cut and keeps the original entries available in session history.
func localCompaction(_ context.Context, request harness.CompactionRequest, _ harness.HookApi) (*harness.CompactionDecision, error) {
	const (
		budget  = 4096
		preview = 360
	)
	var summary strings.Builder
	fmt.Fprintf(&summary, "Compacted history before entry %d. Selected excerpts follow. The original entries remain in session history. Use session_search and session_read for older messages if these tools are available.\n", request.FirstKept)
	if request.Instructions != nil && *request.Instructions != "" {
		fmt.Fprintf(&summary, "\nUser instructions for continuing: %s\n", compactPreview(*request.Instructions, 1024))
	}

	// Select the newest excerpts first, then put them back in transcript order.
	remaining := budget - summary.Len()
	var lines []string
	for _, entry := range slices.Backward(request.Entries) {
		// A previous compact view is derived text, not a new user message.
		if entry.Kind == durable.CompactionEntry.Kind {
			continue
		}
		for _, message := range slices.Backward(entry.Model) {
			role, text := compactText(message)
			if text == "" {
				continue
			}
			line := fmt.Sprintf("\n[%d %s] %s\n", entry.Id, role, compactPreview(text, preview))
			if len(line) > remaining {
				break
			}
			lines = append(lines, line)
			remaining -= len(line)
		}
		if remaining < preview+48 {
			break
		}
	}
	for _, line := range slices.Backward(lines) {
		summary.WriteString(line)
	}
	return &harness.CompactionDecision{Summary: new(summary.String())}, nil
}

func compactText(message ai.Message) (role, text string) {
	switch message := message.(type) {
	case ai.UserMessage:
		switch content := message.Content.(type) {
		case ai.UserText:
			return "user", strings.TrimSpace(string(content))
		case ai.UserContentBlocks:
			for _, block := range content {
				if text, ok := block.(ai.TextContent); ok && strings.TrimSpace(text.Text) != "" {
					return "user", strings.TrimSpace(text.Text)
				}
			}
		}
	case ai.AssistantMessage:
		for _, block := range message.Content {
			if text, ok := block.(ai.TextContent); ok && strings.TrimSpace(text.Text) != "" {
				return "assistant", strings.TrimSpace(text.Text)
			}
		}
	}
	return "", ""
}

func compactPreview(text string, limit int) string {
	const suffix = "…"
	if len(text) <= limit {
		return text
	}
	end := limit - len(suffix)
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + suffix
}
