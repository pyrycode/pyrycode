package control

import (
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRingBuffer_BelowCapacity(t *testing.T) {
	t.Parallel()

	r := NewRingBuffer(5)
	r.Add("a")
	r.Add("b")
	r.Add("c")

	got := r.Snapshot()
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Snapshot = %v, want %v", got, want)
	}
}

func TestRingBuffer_ExactCapacity(t *testing.T) {
	t.Parallel()

	r := NewRingBuffer(3)
	r.Add("a")
	r.Add("b")
	r.Add("c")

	got := r.Snapshot()
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Snapshot = %v, want %v", got, want)
	}
}

func TestRingBuffer_OverflowKeepsRecent(t *testing.T) {
	t.Parallel()

	r := NewRingBuffer(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.Add(s)
	}

	// Oldest two ("a", "b") should have been overwritten.
	got := r.Snapshot()
	want := []string{"c", "d", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Snapshot = %v, want %v", got, want)
	}
}

func TestRingBuffer_SnapshotIsACopy(t *testing.T) {
	t.Parallel()

	r := NewRingBuffer(3)
	r.Add("a")
	r.Add("b")

	snap := r.Snapshot()
	snap[0] = "MUTATED"

	if r.Snapshot()[0] != "a" {
		t.Errorf("ring buffer mutated through Snapshot return value")
	}
}

func TestRingBuffer_MinSizeOne(t *testing.T) {
	t.Parallel()

	r := NewRingBuffer(0)
	if r.Cap() != 1 {
		t.Errorf("Cap with size 0 = %d, want 1 (clamped)", r.Cap())
	}
	r.Add("only")
	r.Add("now")
	if got := r.Snapshot(); !reflect.DeepEqual(got, []string{"now"}) {
		t.Errorf("Snapshot = %v, want [now]", got)
	}
}

func TestSlogTee_WritesToBoth(t *testing.T) {
	t.Parallel()

	ring := NewRingBuffer(10)

	// A discard handler stands in for "stderr" — we don't care about that
	// side here, just that the tee doesn't error and that the ring captures
	// the formatted line.
	logger := slog.New(SlogTee(slog.NewTextHandler(discardWriter{}, nil), ring))
	logger.Info("hello", "key", "value")

	lines := ring.Snapshot()
	if len(lines) != 1 {
		t.Fatalf("ring captured %d lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "hello") {
		t.Errorf("captured line missing message; got %q", lines[0])
	}
	if !strings.Contains(lines[0], "key=value") {
		t.Errorf("captured line missing structured field; got %q", lines[0])
	}
	if strings.HasSuffix(lines[0], "\n") {
		t.Errorf("captured line should be trimmed, ends with newline: %q", lines[0])
	}
}

// TestSlogTee_WithAttrsCarriesThroughTee confirms slog.Logger.With(...)
// produces a sub-logger whose records still get teed into the ring buffer.
// Without forwarding through teeHandler.WithAttrs, records made on the
// sub-logger would skip the ring entirely.
func TestSlogTee_WithAttrsCarriesThroughTee(t *testing.T) {
	t.Parallel()

	ring := NewRingBuffer(10)
	logger := slog.New(SlogTee(slog.NewTextHandler(discardWriter{}, nil), ring))

	sub := logger.With("subsystem", "supervisor")
	sub.Info("spawning", "pid", 42)

	lines := ring.Snapshot()
	if len(lines) != 1 {
		t.Fatalf("ring captured %d lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "subsystem=supervisor") {
		t.Errorf("captured line %q lost the With() attr", lines[0])
	}
	if !strings.Contains(lines[0], "pid=42") {
		t.Errorf("captured line %q lost the per-record attr", lines[0])
	}
}

// TestSlogTee_WithGroupCarriesThroughTee confirms slog.Logger.WithGroup
// nesting also gets teed. Without forwarding through teeHandler.WithGroup,
// grouped attrs would be silently dropped.
func TestSlogTee_WithGroupCarriesThroughTee(t *testing.T) {
	t.Parallel()

	ring := NewRingBuffer(10)
	logger := slog.New(SlogTee(slog.NewTextHandler(discardWriter{}, nil), ring))

	grouped := logger.WithGroup("child")
	grouped.Info("event", "pid", 99)

	lines := ring.Snapshot()
	if len(lines) != 1 {
		t.Fatalf("ring captured %d lines, want 1", len(lines))
	}
	// slog's TextHandler renders WithGroup as "child.pid=99"
	if !strings.Contains(lines[0], "child.pid=99") {
		t.Errorf("captured line %q lost the WithGroup nesting", lines[0])
	}
}

func TestServer_Logs(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ring := NewRingBuffer(10)
	ring.Add("first")
	ring.Add("second")
	ring.Add("third")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, ring, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	resp, err := Logs(context.Background(), sock)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !reflect.DeepEqual(resp.Lines, []string{"first", "second", "third"}) {
		t.Errorf("Lines = %v, want [first second third]", resp.Lines)
	}
	if resp.Capacity != 10 {
		t.Errorf("Capacity = %d, want 10", resp.Capacity)
	}
}

func TestServer_LogsWithoutProvider(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	_, err := Logs(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error when no log provider configured")
	}
	if !strings.Contains(err.Error(), "logs") {
		t.Errorf("error should mention logs, got: %v", err)
	}
}

// discardWriter is an io.Writer that discards everything. Used to silence the
// "primary" handler in slog-tee tests.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// _ = time.Now() — keep the import alive in case future tests need it.
var _ = time.Now
