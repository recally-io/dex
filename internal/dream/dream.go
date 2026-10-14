// Package dream runs dreaming, as in the Agent Memory Repo spec: from time to time, an agent reads
// the chats since the previous dream, adds what repeats across them to the memory, and cleans the
// memory up.
//
// A dream is a hidden chat with one message (channel key chat.DreamChannel + dream ID), so it gets
// the durable delivery of every message: it survives a restart, and the worker commits its memory
// changes. The dreams table records which settled messages each dream read; the next dream starts
// after the newest done one.
package dream

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/vaayne/dex/internal/chat"
	"github.com/vaayne/dex/internal/db/sqlc"
)

const (
	// One dream reads at most this many messages and characters; the next dream reads the rest.
	maxMessages = 500
	maxChars    = 100_000
	// A longer message or answer is cut to this many characters.
	maxEntry = 4_000
)

// Dreamer starts the dreams of one agent.
type Dreamer struct {
	q       *sqlc.Queries
	chats   *chat.Service
	agentID string
	every   time.Duration
	now     func() time.Time
}

// New returns a Dreamer that starts a dream at most once in every period every, and only when the
// agent has a model and the chats have new messages.
func New(q *sqlc.Queries, chats *chat.Service, agentID string, every time.Duration, now func() time.Time) *Dreamer {
	return &Dreamer{q: q, chats: chats, agentID: agentID, every: every, now: now}
}

// Run checks for a dream to start until ctx is done: at once, and then every hour, or every period
// when it is shorter.
func (d *Dreamer) Run(ctx context.Context) {
	tick := time.NewTicker(min(d.every, time.Hour))
	defer tick.Stop()
	for {
		if err := d.start(ctx); err != nil && ctx.Err() == nil {
			slog.Error("start a dream", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (d *Dreamer) start(ctx context.Context) error {
	agent, err := d.q.GetAgent(ctx, d.agentID)
	if err != nil || agent.ModelID == "" {
		return err
	}
	// Prefer a quiet moment. This is not a lock: a chat that starts during the dream runs with it,
	// and both change the memory with the edit tool, which fails on a stale file.
	pending, err := d.q.ListPendingSubmissions(ctx)
	if err != nil || len(pending) > 0 {
		return err
	}
	// One dream at a time, and at most one in each period. A failed dream counts too, so a provider
	// that keeps failing costs at most one attempt in each period.
	latest, err := d.q.LatestDream(ctx, d.agentID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case latest.Status.String == "pending" || d.now().Sub(time.UnixMilli(latest.CreatedAt)) < d.every:
		return nil
	}

	// Without a done dream, the boundary is zero: read from the first message.
	after, err := d.q.DreamBoundary(ctx, d.agentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := d.q.ListDreamInput(ctx, sqlc.ListDreamInputParams{
		AgentID:        d.agentID,
		AfterSettledAt: sql.NullInt64{Int64: after.ThroughSettledAt, Valid: true},
		AfterID:        after.ThroughID,
		MaxRows:        maxMessages,
	})
	if err != nil || len(rows) == 0 {
		return err
	}
	text, last := prompt(rows)

	id := uuid.NewV7().String()
	// The row comes first: a crash before the message is stored leaves a dream without a message,
	// which counts as failed.
	err = d.q.CreateDream(ctx, sqlc.CreateDreamParams{
		ID: id, AgentID: d.agentID, ThroughSettledAt: last.SettledAt, ThroughID: last.ID,
		CreatedAt: d.now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	_, err = d.chats.Post(ctx, d.agentID, chat.DreamChannel+id, id, text)
	return err
}

// prompt returns the dream's message for rows, and the last row that it holds.
func prompt(rows []sqlc.ListDreamInputRow) (string, sqlc.ListDreamInputRow) {
	var b strings.Builder
	b.WriteString(instructions)
	// Group the messages by chat, in the order of each chat's first message.
	var order []int64
	byChat := map[int64][]string{}
	titles := map[int64]string{}
	size := b.Len()
	last := rows[0]
	for i, r := range rows {
		answer := r.Answer.String
		if r.Status != "done" {
			answer = "(no answer: " + cmp.Or(r.Reason.String, "unknown") + ")"
		}
		entry := fmt.Sprintf("- User, %s: %s\n- Answer: %s\n",
			time.UnixMilli(r.CreatedAt).UTC().Format("2006-01-02 15:04 UTC"), cut(r.Content), cut(answer))
		// Keep at least one message, so a dream always makes progress.
		if i > 0 && size+len(entry) > maxChars {
			break
		}
		size += len(entry)
		if _, ok := byChat[r.ChatID]; !ok {
			order = append(order, r.ChatID)
			titles[r.ChatID] = r.Title
		}
		byChat[r.ChatID] = append(byChat[r.ChatID], entry)
		last = r
	}
	for _, id := range order {
		fmt.Fprintf(&b, "\n### dex:chat/%d: %s\n\n%s", id, titles[id], strings.Join(byChat[id], ""))
	}
	return b.String(), last
}

func cut(s string) string {
	if utf8.RuneCountInString(s) <= maxEntry {
		return s
	}
	return string([]rune(s)[:maxEntry]) + " […]"
}

const instructions = `# Dreaming

This is a dream: no user reads this chat. Read the chats below, which are new since the previous dream, and improve your memory. Do two jobs.

1. Add new memory. Find what will help later chats: preferences, decisions, facts, and ways of working, and most of all what repeats across chats. Save each one as an entry. Cite the chats that it comes from, as in [source: dex:chat/12] [source: dex:chat/15], and add [added: YYYY-MM-DD]. Do not save what matters only for one chat.
2. Clean up the memory. Read MEMORY.md and the files that it links. Merge entries that say the same thing. Remove entries that are out of date. When two entries contradict each other, compare their sources and the chats below, and keep the entry that the newer or stronger evidence supports. When the evidence is not clear, keep both and add uncertain: yes to their metadata.

Keep MEMORY.md short, and update the links when you move or rename a file. Follow the memory rules in your instructions. When you finish, answer with a short list of the changes.

## Chats since the previous dream
`
