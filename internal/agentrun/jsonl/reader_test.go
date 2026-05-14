package jsonl

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// Fixtures sourced from ~/.claude/projects/:
//   testdata/clean.jsonl           — -Users-...-architect-15/6fc6d062-...
//   testdata/double_end_turn.jsonl — -Users-...-architect-83/054ce738-...
//   testdata/no_end_turn.jsonl     — -Users-...-code-review-161/08ad9c51-...

func TestReader_CleanSingleEndTurn(t *testing.T) {
	t.Parallel()
	r := newFixtureReader(t, "testdata/clean.jsonl")
	defer closeReader(t, r)

	events, err := drainAll(t, r)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	endOfTurns := 0
	lastEOTIndex := -1
	for i, e := range events {
		if e.EndOfTurn {
			endOfTurns++
			lastEOTIndex = i
		}
	}
	if endOfTurns != 1 {
		t.Fatalf("want exactly 1 EndOfTurn event, got %d", endOfTurns)
	}
	if lastEOTIndex != len(events)-1 {
		t.Fatalf("want EndOfTurn on the last assistant event (index %d), got index %d", len(events)-1, lastEOTIndex)
	}
	if got, want := r.AssistantCount(), 25; got != want {
		t.Fatalf("AssistantCount = %d, want %d", got, want)
	}
}

func TestReader_DoubleEndTurn_FirstSkipped(t *testing.T) {
	t.Parallel()
	r := newFixtureReader(t, "testdata/double_end_turn.jsonl")
	defer closeReader(t, r)

	events, err := drainAll(t, r)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	endTurns := []Event{}
	for _, e := range events {
		if e.StopReason == "end_turn" {
			endTurns = append(endTurns, e)
		}
	}
	if len(endTurns) != 2 {
		t.Fatalf("want 2 end_turn events, got %d", len(endTurns))
	}
	if endTurns[0].TextChars != 0 || endTurns[0].EndOfTurn {
		t.Fatalf("first end_turn must be transitional (TextChars=0, EndOfTurn=false), got TextChars=%d EndOfTurn=%v",
			endTurns[0].TextChars, endTurns[0].EndOfTurn)
	}
	if endTurns[1].TextChars <= 0 || !endTurns[1].EndOfTurn {
		t.Fatalf("second end_turn must fire (TextChars>0, EndOfTurn=true), got TextChars=%d EndOfTurn=%v",
			endTurns[1].TextChars, endTurns[1].EndOfTurn)
	}

	signals := 0
	for _, e := range events {
		if e.EndOfTurn {
			signals++
		}
	}
	if signals != 1 {
		t.Fatalf("want exactly 1 EndOfTurn signal, got %d", signals)
	}
	if got, want := r.AssistantCount(), 6; got != want {
		t.Fatalf("AssistantCount = %d, want %d", got, want)
	}
}

func TestReader_NoEndTurn_SignalNeverFires(t *testing.T) {
	t.Parallel()
	r := newFixtureReader(t, "testdata/no_end_turn.jsonl")
	defer closeReader(t, r)

	events, err := drainAll(t, r)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	for i, e := range events {
		if e.EndOfTurn {
			t.Fatalf("EndOfTurn fired at index %d on a no-end_turn fixture", i)
		}
	}
	// Real assistant-entry count for this fixture (observed): 20.
	if got, want := r.AssistantCount(), 20; got != want {
		t.Fatalf("AssistantCount = %d, want %d", got, want)
	}
}

func TestReader_PartialLine_BuffersUntilNewline(t *testing.T) {
	t.Parallel()

	full := `{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"hello"}]}}` + "\n"
	split := len(full) - 2 // everything except the final `}\n`

	feeder := &stepFeeder{chunks: [][]byte{[]byte(full[:split])}}
	r := NewReader(feeder, Config{})

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("first Next after partial chunk: want io.EOF, got %v", err)
	}
	if got := r.Offset(); got != 0 {
		t.Fatalf("Offset after partial: want 0 (no advance into partial line), got %d", got)
	}

	feeder.push([]byte(full[split:]))

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("second Next after line completed: %v", err)
	}
	if !ev.EndOfTurn {
		t.Fatalf("want EndOfTurn=true, got %+v", ev)
	}
	if got, want := r.Offset(), int64(len(full)); got != want {
		t.Fatalf("Offset after consumption: want %d, got %d", want, got)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after drain: want io.EOF, got %v", err)
	}
}

func TestReader_NonAssistantLinesSkipped(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString(`{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n")
	b.WriteString(`{"type":"system","message":{}}` + "\n")
	b.WriteString(`{"type":"summary","summary":"x"}` + "\n")
	b.WriteString(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}` + "\n")
	src := b.String()

	r := NewReader(strings.NewReader(src), Config{})
	events, err := drainAll(t, r)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if !events[0].EndOfTurn {
		t.Fatalf("want EndOfTurn=true, got %+v", events[0])
	}
	if got, want := r.Offset(), int64(len(src)); got != want {
		t.Fatalf("Offset = %d, want %d (past all four lines)", got, want)
	}
	if got, want := r.AssistantCount(), 1; got != want {
		t.Fatalf("AssistantCount = %d, want %d", got, want)
	}
}

func TestReader_MalformedLineSkippedAndCounted(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString(`{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"text","text":"a"}]}}` + "\n")
	b.WriteString(`{"type":"assistant","message":{` + "\n") // malformed
	b.WriteString(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"bye"}]}}` + "\n")

	rec := &recordingHandler{}
	r := NewReader(strings.NewReader(b.String()), Config{Logger: slog.New(rec)})

	events, err := drainAll(t, r)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if got, want := r.AssistantCount(), 2; got != want {
		t.Fatalf("AssistantCount = %d, want %d", got, want)
	}
	if !events[1].EndOfTurn {
		t.Fatalf("want second event EndOfTurn=true, got %+v", events[1])
	}

	rec.mu.Lock()
	warned := false
	for _, rr := range rec.records {
		if rr.Level == slog.LevelWarn && rr.Message == "jsonl: skipping malformed line" {
			warned = true
			break
		}
	}
	count := len(rec.records)
	rec.mu.Unlock()
	if !warned {
		t.Fatalf("expected malformed-line Warn log; got %d records", count)
	}
}

func TestReader_ResumeFromOffset(t *testing.T) {
	t.Parallel()

	r := newFixtureReader(t, "testdata/clean.jsonl")
	defer closeReader(t, r)

	if _, err := drainAll(t, r); err != nil {
		t.Fatalf("drain: %v", err)
	}
	final := r.Offset()
	if final <= 0 {
		t.Fatalf("expected nonzero final offset, got %d", final)
	}

	r2 := NewReader(strings.NewReader(""), Config{StartOffset: final})
	if got := r2.Offset(); got != final {
		t.Fatalf("Offset before Next: want %d, got %d", final, got)
	}
	if _, err := r2.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next on empty resumed reader: want io.EOF, got %v", err)
	}
	if got := r2.Offset(); got != final {
		t.Fatalf("Offset after EOF: want %d, got %d", final, got)
	}
}

func TestReader_OffsetAdvancesPerLine(t *testing.T) {
	t.Parallel()

	line1 := `{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"text","text":"x"}]}}` + "\n"
	line2 := `{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"y"}]}}` + "\n"
	src := line1 + line2

	r := NewReader(strings.NewReader(src), Config{})
	if _, err := r.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if got, want := r.Offset(), int64(len(line1)); got != want {
		t.Fatalf("Offset after first line: want %d, got %d", want, got)
	}
	if _, err := r.Next(); err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if got, want := r.Offset(), int64(len(src)); got != want {
		t.Fatalf("Offset after second line: want %d, got %d", want, got)
	}
}

// --- helpers ---

func newFixtureReader(t *testing.T, path string) *readerHolder {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return &readerHolder{Reader: NewReader(f, Config{}), f: f}
}

type readerHolder struct {
	*Reader
	f *os.File
}

func closeReader(t *testing.T, r *readerHolder) {
	t.Helper()
	if err := r.f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func drainAll(t *testing.T, r interface {
	Next() (Event, error)
}) ([]Event, error) {
	t.Helper()
	var out []Event
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

// stepFeeder returns queued byte chunks one per Read; when empty, returns
// (0, io.EOF). Caller pushes new chunks via push() to simulate a growing
// file (e.g. fsnotify-driven tail).
type stepFeeder struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (s *stepFeeder) push(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = append(s.chunks, b)
}

func (s *stepFeeder) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	c := s.chunks[0]
	n := copy(p, c)
	if n < len(c) {
		s.chunks[0] = c[n:]
	} else {
		s.chunks = s.chunks[1:]
	}
	return n, nil
}

// recordingHandler is a minimal slog.Handler that captures records for
// inspection. Single-file scope; not exported.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }
