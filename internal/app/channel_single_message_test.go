package app_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUserReceivesOneTelegramSummaryWithoutPreview(t *testing.T) {
	t.Parallel()
	// Given a paired bot and an answer longer than a platform message.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	w := d.watch(t, d.mainSession(t))
	// When the owner sends a message.
	f.message(1, 42, 7, strings.Repeat("stream ", 700)+"END OF ANSWER")
	// Then typing has no text, and the only body is a summary of the final answer.
	if typing := f.nextEvent(t, "sendChatAction"); typing.Text != "" {
		t.Fatalf("typing contains text: %q", typing.Text)
	}
	got := f.next(t)
	if !strings.Contains(got.Text, "Full answer in Web") || strings.Contains(got.Text, "END OF ANSWER") {
		t.Fatalf("expected a summary with a Web reference, received %q", got.Text)
	}
	w.until(t, "full answer in Web", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "assistant" && strings.Contains(item.Text, "END OF ANSWER") {
				return true
			}
		}
		return false
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.requests[0].Method != "sendRichMessage" {
		t.Fatalf("expected only one final message, received %d body requests", len(f.requests))
	}
}

func TestUserReceivesOnlyTheFinalFeishuCard(t *testing.T) {
	t.Parallel()
	// Given a paired Feishu conversation and a run paused in a local tool.
	f := newFakeFeishu(t)
	dir := t.TempDir()
	d := startFeishu(t, f, dir)
	pairFeishu(t, d, f)
	d.setModel(t, "bash")
	f.mu.Lock()
	before := len(f.messages)
	f.mu.Unlock()
	f.text(t, "final-only", "owner", "p2p", taskGate(t, dir, "feishu-card")+"; printf final-only")
	waitTaskFile(t, filepath.Join(dir, "feishu-card.pid"))
	// When the owner waits for the run, no temporary body appears.
	f.mu.Lock()
	pending := len(f.messages) - before
	f.mu.Unlock()
	if pending != 0 {
		t.Fatalf("received %d messages before settlement", pending)
	}
	releaseResultGate(t, dir, "feishu-card")
	// Then the platform receives one final card and no edits.
	f.waitReply(t, "final-only")
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := len(f.messages) - before; got != 1 || f.methods[len(f.methods)-1] != "POST" {
		t.Fatalf("expected one new card, received %d messages via %v", got, f.methods)
	}
}
