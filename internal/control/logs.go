package control

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
)

// LogProvider is the read view the control server depends on for VerbLogs.
// Defining it here (where it is consumed) keeps the supervisor package free
// of control-plane concerns.
type LogProvider interface {
	// Snapshot returns a copy of the recent log lines, oldest first.
	Snapshot() []string
}

// RingBuffer holds the most recent N supervisor log lines for `pyry logs`.
// Bounded — when it fills, new entries overwrite the oldest. Safe under
// concurrent reads and writes.
type RingBuffer struct {
	size    int
	mu      sync.Mutex
	entries []string
	head    int
	full    bool
}

// NewRingBuffer constructs a buffer that holds up to size entries. A size
// less than 1 yields a buffer of size 1 — there is no "disabled" mode.
func NewRingBuffer(size int) *RingBuffer {
	if size < 1 {
		size = 1
	}
	return &RingBuffer{
		size:    size,
		entries: make([]string, size),
	}
}

// Add appends a line. If the buffer is full, the oldest line is overwritten.
func (r *RingBuffer) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.head] = line
	r.head = (r.head + 1) % r.size
	if r.head == 0 {
		r.full = true
	}
}

// Snapshot returns a copy of the entries in order, oldest first.
func (r *RingBuffer) Snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]string, r.head)
		copy(out, r.entries[:r.head])
		return out
	}
	out := make([]string, r.size)
	copy(out, r.entries[r.head:])
	copy(out[r.size-r.head:], r.entries[:r.head])
	return out
}

// Cap returns the configured buffer capacity.
func (r *RingBuffer) Cap() int { return r.size }

// SlogTee returns an slog.Handler that forwards every record to next AND
// writes a text-formatted copy into the ring buffer. Use it to wrap the
// supervisor's primary logger so `pyry logs` can replay recent lifecycle
// events from another shell.
//
// The text format mirrors slog.NewTextHandler with default options — the
// same shape users see on stderr. We construct a fresh text handler per
// record (cheap; this is operational logging, not a hot path).
func SlogTee(next slog.Handler, ring *RingBuffer) slog.Handler {
	return &teeHandler{next: next, ring: ring}
}

type teeHandler struct {
	next slog.Handler
	ring *RingBuffer
}

func (t *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.next.Enabled(ctx, level)
}

func (t *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var buf bytes.Buffer
	txt := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	if err := txt.Handle(ctx, r); err == nil {
		t.ring.Add(strings.TrimRight(buf.String(), "\n"))
	}
	return t.next.Handle(ctx, r)
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{next: t.next.WithAttrs(attrs), ring: t.ring}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{next: t.next.WithGroup(name), ring: t.ring}
}
