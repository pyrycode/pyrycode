package streamjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// newTestEmitter returns an Emitter writing to a fresh bytes.Buffer with a
// deterministic clock that advances 1s between New and Close. The buffer
// already contains the leading init envelope on return; tests asserting on
// emitted-event bytes must skip past it (see initLen below).
func newTestEmitter(t *testing.T) (*Emitter, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	calls := 0
	now := func() time.Time {
		calls++
		// First call: New; second call: Close. Subsequent calls are tolerated.
		return time.Date(2026, 5, 14, 12, 0, calls-1, 0, time.UTC)
	}
	em, err := New(Config{
		Writer:    buf,
		SessionID: "11111111-1111-4111-8111-111111111111",
		Cwd:       "/tmp/test",
		Tools:     []string{"Read"},
		Model:     "test-model",
		Now:       now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return em, buf
}

// entry constructs a non-assistant tuidriver.JSONLEntry with RawLine set to
// rawLine and Type set to t. Mirrors the structural shape produced by
// ptyrunner.eventToEntry for non-assistant kinds.
func entry(t string, rawLine string) tuidriver.JSONLEntry {
	return tuidriver.JSONLEntry{
		Type:    t,
		RawLine: []byte(rawLine),
	}
}

// assistantEntry constructs an assistant tuidriver.JSONLEntry carrying
// stopReason, optional usage (nil to omit), and the synthetic non-empty text
// content tuidriver.IsEndTurn requires when endOfTurn is true. Mirrors the
// structural shape produced by ptyrunner.eventToEntry for assistant kinds.
func assistantEntry(rawLine, stopReason string, usage *usageBlock, endOfTurn bool) tuidriver.JSONLEntry {
	msg := &tuidriver.EntryMessage{StopReason: stopReason}
	if usage != nil {
		msg.Raw = map[string]any{
			"usage": map[string]any{
				"input_tokens":                float64(usage.InputTokens),
				"output_tokens":               float64(usage.OutputTokens),
				"cache_creation_input_tokens": float64(usage.CacheCreationInputTokens),
				"cache_read_input_tokens":     float64(usage.CacheReadInputTokens),
			},
		}
	}
	if endOfTurn {
		msg.Content = []tuidriver.ContentBlock{{
			Type: "text",
			Raw:  map[string]any{"text": "x"},
		}}
	}
	return tuidriver.JSONLEntry{
		Type:    "assistant",
		Message: msg,
		RawLine: []byte(rawLine),
	}
}

// textAssistant builds an assistant entry carrying a single text content
// block with the given text, mirroring the shape tuidriver parses from
// claude's session JSONL. Used by the result-capture tests.
func textAssistant(text, stopReason string) tuidriver.JSONLEntry {
	return tuidriver.JSONLEntry{
		Type: "assistant",
		Message: &tuidriver.EntryMessage{
			StopReason: stopReason,
			Content:    []tuidriver.ContentBlock{{Type: "text", Raw: map[string]any{"text": text}}},
		},
		RawLine: []byte(`{"type":"assistant"}`),
	}
}

func TestNew_ValidatesConfig(t *testing.T) {
	t.Parallel()
	valid := func() Config {
		return Config{
			Writer:    &bytes.Buffer{},
			SessionID: "11111111-1111-4111-8111-111111111111",
			Cwd:       "/tmp/test",
			Tools:     []string{"Read"},
			Model:     "m",
		}
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"nil Writer", func(c *Config) { c.Writer = nil }, "nil Writer"},
		{"empty SessionID", func(c *Config) { c.SessionID = "" }, "empty SessionID"},
		{"empty Cwd", func(c *Config) { c.Cwd = "" }, "empty Cwd"},
		{"nil Tools", func(c *Config) { c.Tools = nil }, "nil Tools"},
		{"empty Model", func(c *Config) { c.Model = "" }, "empty Model"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid()
			tc.mutate(&cfg)
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestEmit_RawPassthrough_PreservesBytesVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		entry tuidriver.JSONLEntry
	}{
		{
			name: "assistant with usage",
			entry: assistantEntry(
				`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":2}}}`,
				"end_turn",
				&usageBlock{InputTokens: 1, OutputTokens: 2},
				true,
			),
		},
		{
			name:  "tool_use no usage",
			entry: entry("tool_use", `{"type":"tool_use","id":"toolu_abc","name":"Read"}`),
		},
		{
			name:  "unrecognised kind preserves non-canonical whitespace",
			entry: entry("", `{"type": "exotic",  "msg":"hi"}`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			em, buf := newTestEmitter(t)
			initLen := buf.Len()
			if err := em.Emit(tc.entry); err != nil {
				t.Fatalf("Emit: %v", err)
			}
			want := append([]byte(nil), tc.entry.RawLine...)
			want = append(want, '\n')
			if !bytes.Equal(buf.Bytes()[initLen:], want) {
				t.Errorf("raw passthrough:\n got  = %q\n want = %q", buf.Bytes()[initLen:], want)
			}
		})
	}
}

func TestEmit_AggregatesUsage(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	entries := []tuidriver.JSONLEntry{
		assistantEntry(`{"type":"assistant","i":1}`, "tool_use",
			&usageBlock{InputTokens: 10, OutputTokens: 1, CacheCreationInputTokens: 100, CacheReadInputTokens: 5}, false),
		entry("user", `{"type":"user"}`),
		// Assistant with no usage — must not crash; spec #353 allows it.
		assistantEntry(`{"type":"assistant","i":2}`, "tool_use", nil, false),
		entry("tool_use", `{"type":"tool_use"}`),
		assistantEntry(`{"type":"assistant","i":3}`, "end_turn",
			&usageBlock{InputTokens: 20, OutputTokens: 3, CacheCreationInputTokens: 200, CacheReadInputTokens: 0}, true),
	}
	for _, ev := range entries {
		if err := em.Emit(ev); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if got := tr.Usage.InputTokens; got != 30 {
		t.Errorf("input_tokens = %d, want 30", got)
	}
	if got := tr.Usage.OutputTokens; got != 4 {
		t.Errorf("output_tokens = %d, want 4", got)
	}
	if got := tr.Usage.CacheCreationInputTokens; got != 300 {
		t.Errorf("cache_creation_input_tokens = %d, want 300", got)
	}
	if got := tr.Usage.CacheReadInputTokens; got != 5 {
		t.Errorf("cache_read_input_tokens = %d, want 5", got)
	}
}

func TestEmit_NumTurnsCountsAssistantEvents(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	kinds := []string{"assistant", "user", "assistant", "tool_use", "assistant", "system", "tool_use", "assistant", "user", "user", "assistant"}
	for i, k := range kinds {
		var ev tuidriver.JSONLEntry
		if k == "assistant" {
			endOfTurn := i == len(kinds)-1
			stop := ""
			if endOfTurn {
				stop = "end_turn"
			}
			ev = assistantEntry(`{}`, stop, nil, endOfTurn)
		} else {
			ev = entry(k, `{}`)
		}
		if err := em.Emit(ev); err != nil {
			t.Fatalf("Emit[%d]: %v", i, err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.NumTurns != 5 {
		t.Errorf("num_turns = %d, want 5", tr.NumTurns)
	}
}

func TestEmit_LastStopReasonWins(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	for _, sr := range []string{"tool_use", "tool_use", "end_turn"} {
		ev := assistantEntry(`{}`, sr, nil, sr == "end_turn")
		if err := em.Emit(ev); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want %q", tr.StopReason, "end_turn")
	}
}

func TestEmit_NoAssistantEvent_StopReasonEmpty(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	for _, k := range []string{"user", "tool_use", "tool_result"} {
		if err := em.Emit(entry(k, `{}`)); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.StopReason != "" {
		t.Errorf("stop_reason = %q, want empty", tr.StopReason)
	}
}

func TestTrailer_CompletionDefault(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	ev := assistantEntry(`{"type":"assistant"}`, "end_turn", nil, true)
	if err := em.Emit(ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Subtype != "success" || tr.TerminalReason != "completed" || tr.IsError {
		t.Errorf("completion trailer: %+v", tr)
	}
}

func TestTrailer_MaxTurns(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	if err := em.Emit(assistantEntry(`{"type":"assistant"}`, "tool_use", nil, false)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	em.SetExitReason(ExitReasonMaxTurns)
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Subtype != "error_max_turns" || tr.TerminalReason != "max_turns" || !tr.IsError {
		t.Errorf("max_turns trailer: %+v", tr)
	}
}

func TestTrailer_Error(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	if err := em.Emit(assistantEntry(`{"type":"assistant"}`, "tool_use", nil, false)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	em.SetExitReason(ExitReasonError)
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Subtype != "error_during_execution" || tr.TerminalReason != "" || !tr.IsError {
		t.Errorf("error trailer: %+v", tr)
	}
}

func TestTrailer_DefaultErrorFallback_NoEOT(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	// Emit only non-EOT events; do NOT call SetExitReason.
	if err := em.Emit(assistantEntry(`{}`, "tool_use", nil, false)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Subtype != "error_during_execution" {
		t.Errorf("default-no-EOT trailer subtype = %q, want error_during_execution", tr.Subtype)
	}
}

func TestSetExitReason_Idempotent(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	em.SetExitReason(ExitReasonMaxTurns)
	em.SetExitReason(ExitReasonError)
	em.SetExitReason("")
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Subtype != "error_max_turns" {
		t.Errorf("first SetExitReason should stick: subtype = %q, want error_max_turns", tr.Subtype)
	}
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	if err := em.Emit(assistantEntry(`{}`, "end_turn", nil, true)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close (1): %v", err)
	}
	snapshot := append([]byte(nil), buf.Bytes()...)
	if err := em.Close(); err != nil {
		t.Fatalf("Close (2): %v", err)
	}
	if !bytes.Equal(buf.Bytes(), snapshot) {
		t.Errorf("second Close wrote bytes:\n before = %q\n after  = %q", snapshot, buf.Bytes())
	}
}

func TestEmit_AfterClose_NoOp(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closedLen := buf.Len()
	if err := em.Emit(assistantEntry(`{"late":"event"}`, "", nil, false)); err != nil {
		t.Errorf("Emit after Close: %v (want nil no-op)", err)
	}
	if buf.Len() != closedLen {
		t.Errorf("Emit after Close wrote bytes: len before=%d, after=%d", closedLen, buf.Len())
	}
}

// failingWriter discards the first failAfter writes and then returns err on
// every subsequent write. failAfter=0 means "fail every write".
type failingWriter struct {
	err       error
	failAfter int
	writes    int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes <= f.failAfter {
		return len(p), nil
	}
	return 0, f.err
}

func TestEmit_WriteErrorIsSticky(t *testing.T) {
	t.Parallel()
	// failAfter=1 so the leading init write succeeds; the first Emit write fails.
	w := &failingWriter{err: errors.New("boom"), failAfter: 1}
	em, err := New(Config{
		Writer:    w,
		SessionID: "11111111-1111-4111-8111-111111111111",
		Cwd:       "/tmp/test",
		Tools:     []string{"Read"},
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := em.Emit(assistantEntry(`{}`, "", nil, false)); err == nil {
		t.Fatal("first Emit: want error, got nil")
	}
	// Second Emit must be a silent no-op — sticky writeErr short-circuits.
	if err := em.Emit(assistantEntry(`{}`, "", nil, false)); err != nil {
		t.Errorf("second Emit: %v (want nil no-op)", err)
	}
	// 1 init + 1 Emit write that failed = 2 total Write calls; the second
	// Emit's sticky-short-circuit must NOT touch the writer.
	if w.writes != 2 {
		t.Errorf("failing writer was called %d times, want 2 (init + first emit only)", w.writes)
	}
}

func TestNew_InitWriteFailureReturnsError(t *testing.T) {
	t.Parallel()
	w := &failingWriter{err: errors.New("boom")}
	em, err := New(Config{
		Writer:    w,
		SessionID: "11111111-1111-4111-8111-111111111111",
		Cwd:       "/tmp/test",
		Tools:     []string{"Read"},
		Model:     "test-model",
	})
	if err == nil {
		t.Fatal("New: want error, got nil")
	}
	if !strings.Contains(err.Error(), "streamjson: emit init") {
		t.Errorf("err = %v, want substring %q", err, "streamjson: emit init")
	}
	if !errors.Is(err, w.err) {
		t.Errorf("err = %v, want wrapped %v", err, w.err)
	}
	if em != nil {
		t.Errorf("New returned non-nil emitter on init write failure: %+v", em)
	}
}

func TestTrailer_DurationMSUsesNowSeam(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	calls := 0
	now := func() time.Time {
		calls++
		// First call (New) at t=0; second call (Close) at t=1500ms.
		if calls == 1 {
			return time.Unix(0, 0)
		}
		return time.Unix(0, 0).Add(1500 * time.Millisecond)
	}
	em, err := New(Config{
		Writer:    buf,
		SessionID: "11111111-1111-4111-8111-111111111111",
		Cwd:       "/tmp/test",
		Tools:     []string{"Read"},
		Model:     "test-model",
		Now:       now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.DurationMS != 1500 {
		t.Errorf("duration_ms = %d, want 1500", tr.DurationMS)
	}
}

func TestTrailer_SessionIDRoundTrips(t *testing.T) {
	t.Parallel()
	const id = "deadbeef-dead-4ead-aead-deaddeadbeef"
	buf := &bytes.Buffer{}
	em, err := New(Config{
		Writer:    buf,
		SessionID: id,
		Cwd:       "/tmp/test",
		Tools:     []string{"Read"},
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.SessionID != id {
		t.Errorf("session_id = %q, want %q", tr.SessionID, id)
	}
}

func TestTrailer_ConstantFields(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Type != "result" {
		t.Errorf("type = %q, want result", tr.Type)
	}
	if tr.Result != "" {
		t.Errorf("result = %q, want empty", tr.Result)
	}
	if tr.TotalCostUSD != 0 {
		t.Errorf("total_cost_usd = %v, want 0", tr.TotalCostUSD)
	}
}

// TestTrailer_ResultCapturesFinalAssistantText pins the parity fix: the
// trailer's `result` field carries the LAST assistant turn's text, matching
// what claude -p / streamrunner emit. Intermediate "let me check…" turns are
// overwritten by the end-of-turn summary; the textless tool_use turn between
// them must not clobber the captured text. (Regression: ptyrunner shipped an
// empty `result`, blanking the dispatcher's completion-comment embed —
// surfaced 2026-05-29.)
func TestTrailer_ResultCapturesFinalAssistantText(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	for _, ev := range []tuidriver.JSONLEntry{
		textAssistant("Let me inspect the ticket.", "tool_use"),
		entry("tool_use", `{"type":"tool_use"}`),
		textAssistant("## Done — refined and unblocked.", "end_turn"),
	} {
		if err := em.Emit(ev); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Result != "## Done — refined and unblocked." {
		t.Errorf("result = %q, want the final assistant summary", tr.Result)
	}
}

// TestTrailer_ResultEmptyWhenNoAssistantText guards the "last non-empty wins"
// rule's floor: assistant turns with no text content (tool_use-only) plus
// non-assistant lines leave result empty rather than panicking or emitting
// stray bytes. Keeps the no-text path identical to the pre-fix behaviour.
func TestTrailer_ResultEmptyWhenNoAssistantText(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	for _, ev := range []tuidriver.JSONLEntry{
		assistantEntry(`{}`, "tool_use", nil, false),
		entry("tool_use", `{"type":"tool_use"}`),
		entry("user", `{"type":"user"}`),
	} {
		if err := em.Emit(ev); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.Result != "" {
		t.Errorf("result = %q, want empty (no assistant text emitted)", tr.Result)
	}
}

// TestReadUsage_NilMessage covers the absent path where an assistant entry
// arrives without a Message (malformed envelope). readUsage must report
// absent (not panic), and the rest of the assistant state machine must still
// update.
func TestReadUsage_NilMessage(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	ev := tuidriver.JSONLEntry{
		Type:    "assistant",
		Message: nil,
		RawLine: []byte(`{"type":"assistant"}`),
	}
	if err := em.Emit(ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.NumTurns != 1 {
		t.Errorf("num_turns = %d, want 1", tr.NumTurns)
	}
	assertZeroUsage(t, tr)
}

// TestReadUsage_NoRawMap covers the absent path where Message exists but
// Message.Raw is nil.
func TestReadUsage_NoRawMap(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	ev := tuidriver.JSONLEntry{
		Type:    "assistant",
		Message: &tuidriver.EntryMessage{StopReason: "tool_use", Raw: nil},
		RawLine: []byte(`{"type":"assistant"}`),
	}
	if err := em.Emit(ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.NumTurns != 1 {
		t.Errorf("num_turns = %d, want 1", tr.NumTurns)
	}
	assertZeroUsage(t, tr)
}

// TestReadUsage_NoUsageKey covers the absent path where Message.Raw has no
// "usage" key.
func TestReadUsage_NoUsageKey(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	ev := tuidriver.JSONLEntry{
		Type: "assistant",
		Message: &tuidriver.EntryMessage{
			StopReason: "tool_use",
			Raw:        map[string]any{"stop_reason": "tool_use"},
		},
		RawLine: []byte(`{"type":"assistant"}`),
	}
	if err := em.Emit(ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.NumTurns != 1 {
		t.Errorf("num_turns = %d, want 1", tr.NumTurns)
	}
	assertZeroUsage(t, tr)
}

// TestReadUsage_NonMapUsage covers the absent path where "usage" exists but
// is not a JSON object (defensive against malformed claude output).
func TestReadUsage_NonMapUsage(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	ev := tuidriver.JSONLEntry{
		Type: "assistant",
		Message: &tuidriver.EntryMessage{
			StopReason: "tool_use",
			Raw:        map[string]any{"usage": "not a map"},
		},
		RawLine: []byte(`{"type":"assistant"}`),
	}
	if err := em.Emit(ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tr := lastTrailer(t, buf.Bytes())
	if tr.NumTurns != 1 {
		t.Errorf("num_turns = %d, want 1", tr.NumTurns)
	}
	assertZeroUsage(t, tr)
}

func assertZeroUsage(t *testing.T, tr trailer) {
	t.Helper()
	if tr.Usage.InputTokens != 0 || tr.Usage.OutputTokens != 0 ||
		tr.Usage.CacheCreationInputTokens != 0 || tr.Usage.CacheReadInputTokens != 0 {
		t.Errorf("usage totals not all zero: %+v", tr.Usage)
	}
}

// lastTrailer decodes the final non-empty line of buf as a trailer struct.
func lastTrailer(t *testing.T, buf []byte) trailer {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(buf, "\n"), []byte("\n"))
	var tr trailer
	if err := json.Unmarshal(lines[len(lines)-1], &tr); err != nil {
		t.Fatalf("decode trailer %q: %v", lines[len(lines)-1], err)
	}
	if tr.Type != "result" {
		t.Fatalf("last line is not a result trailer: %s", lines[len(lines)-1])
	}
	return tr
}

// lineToEntry parses one already-trimmed JSONL line into a tuidriver.JSONLEntry,
// duplicating tuidriver's package-internal parseEntry/parseMessage logic. This
// keeps the captured-fixture replay test independent of jsonl.Reader (which
// #512 deletes). A future tuidriver.ParseEntry export removes this duplication.
func lineToEntry(line []byte) tuidriver.JSONLEntry {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return tuidriver.JSONLEntry{}
	}
	entry := tuidriver.JSONLEntry{Raw: raw, RawLine: bytes.Clone(line)}
	entry.Type, _ = raw["type"].(string)
	m, ok := raw["message"].(map[string]any)
	if !ok {
		return entry
	}
	msg := tuidriver.EntryMessage{Raw: m}
	msg.ID, _ = m["id"].(string)
	msg.StopReason, _ = m["stop_reason"].(string)
	if blocks, ok := m["content"].([]any); ok {
		msg.Content = make([]tuidriver.ContentBlock, 0, len(blocks))
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			cb := tuidriver.ContentBlock{Raw: bm}
			cb.Type, _ = bm["type"].(string)
			msg.Content = append(msg.Content, cb)
		}
	}
	entry.Message = &msg
	return entry
}

// TestCapturedFixture_ByteEquivalence replays the non-result lines from
// testdata/captured_run.jsonl through the emitter, calls Close, and asserts:
//
//	(a) each replayed non-result line is byte-equivalent to its fixture
//	    counterpart;
//	(b) the trailer pyry composes matches the fixture's result line on the
//	    field set defined by this spec (type, subtype, is_error, num_turns,
//	    stop_reason, terminal_reason, and the four usage int counters).
//
// SYNTHESISED FIXTURE: testdata/captured_run.jsonl was hand-crafted to match
// the shapes documented in the spec. The captured `claude -p` run was not
// available in the developer environment at #354 implementation time (Phase
// D of the migration drops `claude -p` as a dependency); the fixture is
// schema-equivalent to the captured trailers reproduced verbatim in the
// architect's spec.
//
// Known-expected diff list (fields claude emits that pyry does NOT, and so
// are not compared): modelUsage, permission_denials, fast_mode_state, uuid,
// errors, api_error_status, duration_api_ms, and the extra usage keys
// (server_tool_use, service_tier, cache_creation, inference_geo, iterations,
// speed). Intentionally different: session_id, duration_ms, total_cost_usd.
func TestCapturedFixture_ByteEquivalence(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "captured_run.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Split on '\n' but preserve trailing '\r' if any (tuidriver does).
	allLines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(allLines) < 2 {
		t.Fatalf("fixture has %d lines, want >=2 (events + trailer)", len(allLines))
	}
	nonResult := allLines[:len(allLines)-1]
	fixtureResultLine := allLines[len(allLines)-1]

	buf := &bytes.Buffer{}
	em, err := New(Config{
		Writer:    buf,
		SessionID: "28b6666c-aaaa-4aaa-baaa-aaaaaaaaaaaa",
		Cwd:       "/tmp/example",
		Tools:     []string{"Read", "Bash"},
		Model:     "claude-haiku-4-5",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The fixture's leading init line is produced by New itself; skip it
	// here to avoid duplication. Every other non-result line drives Emit.
	for _, line := range nonResult[1:] {
		if line == "" {
			continue
		}
		entry := lineToEntry([]byte(line))
		if err := em.Emit(entry); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(out) != len(nonResult)+1 {
		t.Fatalf("pyry stdout has %d lines, want %d (init + non-init non-result + trailer)", len(out), len(nonResult)+1)
	}
	// (a) non-result lines byte-equivalent — including the producer-side
	// init at index 0, which New synthesises from the same field values
	// captured in the fixture's first line.
	for i, want := range nonResult {
		if string(out[i]) != want {
			t.Errorf("line %d not byte-equivalent:\n got  = %q\n want = %q", i, out[i], want)
		}
	}

	// (b) trailer field equivalence on the agreed schema subset.
	var pyryTrailer, fixTrailer map[string]any
	if err := json.Unmarshal(out[len(out)-1], &pyryTrailer); err != nil {
		t.Fatalf("decode pyry trailer: %v", err)
	}
	if err := json.Unmarshal([]byte(fixtureResultLine), &fixTrailer); err != nil {
		t.Fatalf("decode fixture trailer: %v", err)
	}
	for _, k := range []string{"type", "subtype", "is_error", "num_turns", "stop_reason", "terminal_reason"} {
		if !equalAny(pyryTrailer[k], fixTrailer[k]) {
			t.Errorf("trailer[%q]: pyry=%v, fixture=%v", k, pyryTrailer[k], fixTrailer[k])
		}
	}
	// usage subset comparison
	pu, _ := pyryTrailer["usage"].(map[string]any)
	fu, _ := fixTrailer["usage"].(map[string]any)
	for _, k := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		if !equalAny(pu[k], fu[k]) {
			t.Errorf("usage[%q]: pyry=%v, fixture=%v", k, pu[k], fu[k])
		}
	}
}

// equalAny compares two json-decoded values. json.Unmarshal into map[string]any
// surfaces numbers as float64, so direct == on ints would fail.
func equalAny(a, b any) bool {
	return a == b
}

// TestNew_WritesInitLineFirst asserts the first newline-terminated line
// written by New decodes to the init shape with field values matching the
// Config inputs verbatim.
func TestNew_WritesInitLineFirst(t *testing.T) {
	t.Parallel()
	const (
		sid   = "deadbeef-dead-4ead-aead-deaddeadbeef"
		cwd   = "/tmp/test-cwd"
		model = "claude-haiku-4-5"
	)
	tools := []string{"Read", "Bash", "Glob"}

	buf := &bytes.Buffer{}
	em, err := New(Config{
		Writer:    buf,
		SessionID: sid,
		Cwd:       cwd,
		Tools:     tools,
		Model:     model,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = em

	idx := bytes.IndexByte(buf.Bytes(), '\n')
	if idx < 0 {
		t.Fatalf("New did not write a newline-terminated init line: %q", buf.Bytes())
	}
	var got initLine
	if err := json.Unmarshal(buf.Bytes()[:idx], &got); err != nil {
		t.Fatalf("decode init line %q: %v", buf.Bytes()[:idx], err)
	}
	want := initLine{
		Type:      "system",
		Subtype:   "init",
		Cwd:       cwd,
		Tools:     tools,
		Model:     model,
		SessionID: sid,
	}
	if got.Type != want.Type || got.Subtype != want.Subtype ||
		got.Cwd != want.Cwd || got.Model != want.Model || got.SessionID != want.SessionID {
		t.Errorf("init line:\n got  = %+v\n want = %+v", got, want)
	}
	if !reflect.DeepEqual(got.Tools, want.Tools) {
		t.Errorf("init tools: got %v, want %v", got.Tools, want.Tools)
	}
}

// TestNew_InitLineKeyOrderMatchesFixture locks the struct's tag declaration
// order to the captured fixture's first line. JSON-parsing into a map would
// lose order; we walk the byte prefix instead. A future contributor
// reordering initLine's fields will break this test with a diagnostic
// pointing at the fixture as the source of truth.
func TestNew_InitLineKeyOrderMatchesFixture(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "captured_run.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fixtureLine0 := bytes.SplitN(raw, []byte("\n"), 2)[0]

	buf := &bytes.Buffer{}
	if _, err := New(Config{
		Writer:    buf,
		SessionID: "28b6666c-aaaa-4aaa-baaa-aaaaaaaaaaaa",
		Cwd:       "/tmp/example",
		Tools:     []string{"Read", "Bash"},
		Model:     "claude-haiku-4-5",
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
	idx := bytes.IndexByte(buf.Bytes(), '\n')
	if idx < 0 {
		t.Fatalf("no init line written")
	}
	gotInit := buf.Bytes()[:idx]
	if !bytes.Equal(gotInit, fixtureLine0) {
		t.Errorf("init line does not match fixture (captured_run.jsonl:1):\n got  = %s\n want = %s", gotInit, fixtureLine0)
	}
}

// TestNew_EmptyToolsMarshalsAsEmptyArray pins that a non-nil empty Tools
// slice marshals as `[]`, not `null`. Consumers anchoring on the field's
// presence-as-array would break on `null`.
func TestNew_EmptyToolsMarshalsAsEmptyArray(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	if _, err := New(Config{
		Writer:    buf,
		SessionID: "11111111-1111-4111-8111-111111111111",
		Cwd:       "/tmp/test",
		Tools:     []string{},
		Model:     "m",
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
	idx := bytes.IndexByte(buf.Bytes(), '\n')
	if idx < 0 {
		t.Fatalf("no init line written")
	}
	if !bytes.Contains(buf.Bytes()[:idx], []byte(`"tools":[]`)) {
		t.Errorf("init line missing `\"tools\":[]`:\n %s", buf.Bytes()[:idx])
	}
	if bytes.Contains(buf.Bytes()[:idx], []byte(`"tools":null`)) {
		t.Errorf("init line marshalled empty Tools as null:\n %s", buf.Bytes()[:idx])
	}
}
