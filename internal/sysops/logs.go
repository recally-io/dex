// Package sysops owns in-process server operations: updates, restart coordination, backups, and logs.
package sysops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

const defaultLogCapacity = 500

// LogEntry is one captured structured log line.
type LogEntry struct {
	Seq     int64
	Time    int64
	Level   string
	Message string
	Attrs   string
}

// LogBuffer keeps the most recent structured log entries in a fixed-size ring buffer.
type LogBuffer struct {
	mu      sync.Mutex
	entries []LogEntry
	next    int
	count   int
	seq     int64
}

// NewLogBuffer returns a ring buffer with the given capacity (default 500).
func NewLogBuffer(capacity int) *LogBuffer {
	if capacity <= 0 {
		capacity = defaultLogCapacity
	}
	return &LogBuffer{entries: make([]LogEntry, capacity)}
}

// Add appends one entry to the ring buffer.
func (b *LogBuffer) Add(entry LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	entry.Seq = b.seq
	b.entries[b.next] = entry
	b.next = (b.next + 1) % len(b.entries)
	if b.count < len(b.entries) {
		b.count++
	}
}

// Entries returns up to limit entries in chronological order (oldest to newest).
func (b *LogBuffer) Entries(limit int) []LogEntry {
	if b == nil {
		return []LogEntry{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count == 0 {
		return []LogEntry{}
	}
	n := b.count
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]LogEntry, n)
	capLen := len(b.entries)
	start := (b.next - n + capLen) % capLen
	for i := range n {
		out[i] = b.entries[(start+i)%capLen]
	}
	return out
}

// TeeHandler wraps inner and copies every handled slog record into buf.
func TeeHandler(inner slog.Handler, buf *LogBuffer) slog.Handler {
	if buf == nil {
		return inner
	}
	return &teeHandler{inner: inner, buf: buf}
}

type teeHandler struct {
	inner  slog.Handler
	buf    *LogBuffer
	attrs  []slog.Attr
	groups []string
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level >= slog.LevelInfo {
		return true
	}
	return h.inner != nil && h.inner.Enabled(ctx, level)
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var parts []string
	prefix := ""
	if len(h.groups) > 0 {
		prefix = strings.Join(h.groups, ".") + "."
	}
	for _, a := range h.attrs {
		if text := formatAttr(prefix, a); text != "" {
			parts = append(parts, text)
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if text := formatAttr(prefix, a); text != "" {
			parts = append(parts, text)
		}
		return true
	})
	h.buf.Add(LogEntry{
		Time:    r.Time.UnixMilli(),
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   strings.Join(parts, " "),
	})
	if h.inner != nil && h.inner.Enabled(ctx, r.Level) {
		return h.inner.Handle(ctx, r)
	}
	return nil
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &teeHandler{
		buf:    h.buf,
		attrs:  append(append([]slog.Attr(nil), h.attrs...), attrs...),
		groups: append([]string(nil), h.groups...),
	}
	if h.inner != nil {
		next.inner = h.inner.WithAttrs(attrs)
	}
	return next
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := &teeHandler{
		buf:    h.buf,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append(append([]string(nil), h.groups...), name),
	}
	if h.inner != nil {
		next.inner = h.inner.WithGroup(name)
	}
	return next
}

func formatAttr(prefix string, a slog.Attr) string {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return ""
	}
	return fmt.Sprintf("%s%s=%s", prefix, a.Key, a.Value.String())
}
