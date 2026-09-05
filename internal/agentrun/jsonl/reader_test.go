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
	lastAssistantIndex := -1
	for i, e := range events {
		if e.Kind == "assistant" {
			lastAssistantIndex = i
		}
		if e.EndOfTurn {
			endOfTurns++
			lastEOTIndex = i
		}
	}
	if endOfTurns != 1 {
		t.Fatalf("want exactly 1 EndOfTurn event, got %d", endOfTurns)
	}
	if lastEOTIndex != lastAssistantIndex {
		t.Fatalf("want EndOfTurn on the last assistant event (index %d), got index %d", lastAssistantIndex, lastEOTIndex)
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

func TestReader_NonAssistantLinesSurfaced(t *testing.T) {
	t.Parallel()

	lines := []string{
		`{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"system","message":{}}`,
		`{"type":"summary","summary":"x"}`,
		`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
	}
	src := strings.Join(lines, "\n") + "\n"

	r := NewReader(strings.NewReader(src), Config{})
	events, err := drainAll(t, r)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("want 4 events, got %d", len(events))
	}
	wantKinds := []string{"user", "system", "", "assistant"}
	for i, ev := range events {
		if ev.Kind != wantKinds[i] {
			t.Fatalf("events[%d].Kind = %q, want %q", i, ev.Kind, wantKinds[i])
		}
		if string(ev.Raw) != lines[i] {
			t.Fatalf("events[%d].Raw = %q, want %q", i, string(ev.Raw), lines[i])
		}
	}
	// Only the last (assistant end_turn) event signals end-of-turn.
	for i, ev := range events[:3] {
		if ev.EndOfTurn || ev.StopReason != "" || ev.TextChars != 0 || ev.Usage != nil {
			t.Fatalf("events[%d] non-assistant should be zero-valued, got %+v", i, ev)
		}
	}
	if !events[3].EndOfTurn {
		t.Fatalf("want events[3].EndOfTurn=true, got %+v", events[3])
	}
	if got, want := r.Offset(), int64(len(src)); got != want {
		t.Fatalf("Offset = %d, want %d (past all four lines)", got, want)
	}
	if got, want := r.AssistantCount(), 1; got != want {
		t.Fatalf("AssistantCount = %d, want %d", got, want)
	}
}

func TestReader_RawByteEquivalence(t *testing.T) {
	t.Parallel()

	lines := []string{
		`{"type":"user","message":{"content":[{"type":"text","text":"héllo 世界"}]}}`,
		`{"type":"tool_use","message":{"id":"abc","input":{"nested":{"k":"v"}}}}`,
		`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"  spaces  "}]}}`,
	}
	src := strings.Join(lines, "\n") + "\n"
	r := NewReader(strings.NewReader(src), Config{})

	// Read all events first, then inspect Raw — this catches a buffer-aliasing
	// regression where Event.Raw shares memory with the reader's internal buf.
	events, err := drainAll(t, r)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != len(lines) {
		t.Fatalf("got %d events, want %d", len(events), len(lines))
	}
	for i, ev := range events {
		if string(ev.Raw) != lines[i] {
			t.Fatalf("events[%d].Raw = %q, want %q", i, string(ev.Raw), lines[i])
		}
	}
}

func TestReader_KindClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		want string
	}{
		{"assistant", `{"type":"assistant","message":{"stop_reason":"tool_use","content":[]}}`, "assistant"},
		{"user", `{"type":"user","message":{"content":"hi"}}`, "user"},
		{"tool_use", `{"type":"tool_use","id":"a"}`, "tool_use"},
		{"tool_result", `{"type":"tool_result","id":"a"}`, "tool_result"},
		{"system", `{"type":"system","message":{}}`, "system"},
		{"attachment", `{"type":"attachment","path":"/x"}`, "attachment"},
		{"summary unknown", `{"type":"summary","summary":"x"}`, ""},
		{"missing type", `{}`, ""},
		{"unrelated field only", `{"foo":"bar"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(tc.line+"\n"), Config{})
			ev, err := r.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if ev.Kind != tc.want {
				t.Fatalf("Kind = %q, want %q", ev.Kind, tc.want)
			}
		})
	}
}

func TestReader_UsageParsedOnAssistant(t *testing.T) {
	t.Parallel()

	line := `{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":11,"output_tokens":22,"cache_creation_input_tokens":33,"cache_read_input_tokens":44}}}`
	r := NewReader(strings.NewReader(line+"\n"), Config{})
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Usage == nil {
		t.Fatalf("Usage = nil, want non-nil")
	}
	want := UsageBlock{InputTokens: 11, OutputTokens: 22, CacheCreationInputTokens: 33, CacheReadInputTokens: 44}
	if *ev.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", *ev.Usage, want)
	}
}

func TestReader_UsageNilOnAssistantWithoutUsage(t *testing.T) {
	t.Parallel()

	line := `{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`
	r := NewReader(strings.NewReader(line+"\n"), Config{})
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Usage != nil {
		t.Fatalf("Usage = %+v, want nil", *ev.Usage)
	}
}

func TestReader_UsageNilOnNonAssistant(t *testing.T) {
	t.Parallel()

	// Defensive: even if a non-assistant line carries a usage-shaped object,
	// the reader contract says Usage is always nil on non-assistant Events.
	line := `{"type":"user","message":{"content":"hi","usage":{"input_tokens":1,"output_tokens":2}}}`
	r := NewReader(strings.NewReader(line+"\n"), Config{})
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Kind != "user" {
		t.Fatalf("Kind = %q, want %q", ev.Kind, "user")
	}
	if ev.Usage != nil {
		t.Fatalf("Usage = %+v, want nil", *ev.Usage)
	}
}

// TestReader_ModelSurfacedOnAssistant covers the join key #2107 reads: the model
// that produced an entry's usage block, verbatim, so a consumer can pair the used
// count against the window claude reported for that same model.
func TestReader_ModelSurfacedOnAssistant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "assistant with model",
			line: `{"type":"assistant","message":{"model":"claude-sonnet-5","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
			want: "claude-sonnet-5",
		},
		{
			// An entry with no model names no model. It must NOT be joinable
			// against a report entry keyed by the empty string, which #2101
			// retains when its window is positive — see contextwindow.Read, where
			// that rule is enforced.
			name: "assistant without model",
			line: `{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
			want: "",
		},
		{
			// Defensive, mirroring TestReader_UsageNilOnNonAssistant: even a
			// non-assistant line carrying a model-shaped field surfaces "".
			name: "non-assistant line carrying a model field",
			line: `{"type":"user","message":{"model":"claude-sonnet-5","content":"hi"}}`,
			want: "",
		},
		{
			// 256 and 257 are LITERALS, deliberately not maxModelIDBytes and
			// maxModelIDBytes+1: writing a cap's own test in terms of the cap
			// moves fixture and expectation in lockstep, so a mutant that
			// retunes the constant survives green. The literal is what pins the
			// number to streamsup's maxModelWindowID, which is the only reason
			// this bound is 256 at all.
			name: "exactly at the bound survives whole",
			line: `{"type":"assistant","message":{"model":"` + strings.Repeat("m", 256) + `","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
			want: strings.Repeat("m", 256),
		},
		{
			// The assertion that matters: an over-bound id yields NO key, never a
			// cut one. A truncating implementation would surface the 256-byte
			// prefix here and pass a mere "!= the full value" check, while
			// turning a guaranteed miss into a possible false match against a
			// different model whose id IS that prefix.
			name: "over the bound yields no key, not a prefix",
			line: `{"type":"assistant","message":{"model":"` + strings.Repeat("m", 257) + `","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := NewReader(strings.NewReader(tt.line+"\n"), Config{})
			ev, err := r.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if ev.Model != tt.want {
				t.Errorf("Model = %q (len %d), want %q (len %d)",
					ev.Model, len(ev.Model), tt.want, len(tt.want))
			}
		})
	}
}

// TestReader_NonStringModelSkipsTheEntry pins the cost of widening
// rawAssistantMessage by one field, so the behaviour is chosen rather than
// stumbled into (#2107 security review, [Error messages, logs, telemetry]).
//
// Before the model field existed, "model" was an unknown key and encoding/json
// skipped it whatever its type. Now a non-string value fails the whole message
// unmarshal, so the entry is logged-and-skipped and its USAGE BLOCK is lost —
// contextwindow.Read then reports an older entry's count. That is degraded
// toward the fresh-session reading, never a wrong-and-confident answer, and it
// is the failure mode stop_reason has always carried on the same struct.
//
// The second line proves the skip is per-line rather than terminal: iteration
// continues and the next entry surfaces normally.
func TestReader_NonStringModelSkipsTheEntry(t *testing.T) {
	t.Parallel()

	lines := `{"type":"assistant","message":{"model":123,"stop_reason":"end_turn","content":[{"type":"text","text":"lost"}],"usage":{"input_tokens":9}}}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-sonnet-5","stop_reason":"end_turn","content":[{"type":"text","text":"kept"}]}}` + "\n"
	r := NewReader(strings.NewReader(lines), Config{Logger: slog.New(&recordingHandler{})})

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Model != "claude-sonnet-5" {
		t.Fatalf("first surfaced Model = %q, want the SECOND line's %q — a non-string model must skip its own entry only",
			ev.Model, "claude-sonnet-5")
	}
	if ev.Usage != nil {
		t.Errorf("Usage = %+v on the surviving entry, want nil — the skipped entry's usage must not leak forward", *ev.Usage)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("third Next err = %v, want io.EOF", err)
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
