package server

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LogEntry is one captured log record, JSON-serialized for the Logs screen.
type LogEntry struct {
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// LogRing is a fixed-size, thread-safe ring buffer of the most recent log
// entries, exposed to the UI via GET /api/logs.
type LogRing struct {
	mu  sync.Mutex
	buf []LogEntry
	max int
}

// NewLogRing returns a ring holding at most max entries (min 1).
func NewLogRing(max int) *LogRing {
	if max < 1 {
		max = 1
	}
	return &LogRing{max: max}
}

func (r *LogRing) append(e LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, e)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
}

// Entries returns a copy of the buffered entries, oldest first.
func (r *LogRing) Entries() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LogEntry, len(r.buf))
	copy(out, r.buf)
	return out
}

// Clear drops all buffered entries.
func (r *LogRing) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = nil
}

// NewRingHandler builds an slog.Handler that writes every (enabled) record to
// both base (typically a text handler on stderr) and the ring buffer. Level
// filtering is whatever base enforces (wire base with the shared LevelVar).
func NewRingHandler(base slog.Handler, ring *LogRing) slog.Handler {
	return &ringHandler{base: base, ring: ring}
}

type ringHandler struct {
	base  slog.Handler
	ring  *LogRing
	attrs []slog.Attr
}

func (h *ringHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.base.Enabled(ctx, l)
}

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs()+len(h.attrs))
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.ring.append(LogEntry{
		Time:  r.Time,
		Level: r.Level.String(),
		Msg:   r.Message,
		Attrs: attrs,
	})
	return h.base.Handle(ctx, r)
}

func (h *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(as))
	merged = append(merged, h.attrs...)
	merged = append(merged, as...)
	return &ringHandler{base: h.base.WithAttrs(as), ring: h.ring, attrs: merged}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{base: h.base.WithGroup(name), ring: h.ring, attrs: h.attrs}
}
