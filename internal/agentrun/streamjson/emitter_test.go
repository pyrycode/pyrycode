package streamjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

// newTestEmitter returns an Emitter writing to a fresh bytes.Buffer with a
// deterministic clock that advances 1s between New and Close.
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
		Now:       now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return em, buf
}

func TestNew_ValidatesConfig(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{SessionID: "x"}); err == nil {
		t.Error("New(nil writer): want error")
	}
	if _, err := New(Config{Writer: &bytes.Buffer{}}); err == nil {
		t.Error("New(empty SessionID): want error")
	}
}

func TestEmit_RawPassthrough_PreservesBytesVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ev   jsonl.Event
	}{
		{
			name: "assistant with usage",
			ev: jsonl.Event{
				Kind:       "assistant",
				StopReason: "end_turn",
				EndOfTurn:  true,
				Raw:        json.RawMessage(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":2}}}`),
				Usage:      &jsonl.UsageBlock{InputTokens: 1, OutputTokens: 2},
			},
		},
		{
			name: "tool_use no usage",
			ev: jsonl.Event{
				Kind: "tool_use",
				Raw:  json.RawMessage(`{"type":"tool_use","id":"toolu_abc","name":"Read"}`),
			},
		},
		{
			name: "unrecognised kind preserves non-canonical whitespace",
			ev: jsonl.Event{
				Kind: "",
				Raw:  json.RawMessage(`{"type": "exotic",  "msg":"hi"}`),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			em, buf := newTestEmitter(t)
			if err := em.Emit(tc.ev); err != nil {
				t.Fatalf("Emit: %v", err)
			}
			want := append([]byte(nil), tc.ev.Raw...)
			want = append(want, '\n')
			if !bytes.Equal(buf.Bytes(), want) {
				t.Errorf("raw passthrough:\n got  = %q\n want = %q", buf.Bytes(), want)
			}
		})
	}
}

func TestEmit_AggregatesUsage(t *testing.T) {
	t.Parallel()
	em, buf := newTestEmitter(t)
	events := []jsonl.Event{
		{
			Kind: "assistant", StopReason: "tool_use",
			Raw:   json.RawMessage(`{"type":"assistant","i":1}`),
			Usage: &jsonl.UsageBlock{InputTokens: 10, OutputTokens: 1, CacheCreationInputTokens: 100, CacheReadInputTokens: 5},
		},
		{
			Kind: "user",
			Raw:  json.RawMessage(`{"type":"user"}`),
		},
		{
			Kind: "assistant", StopReason: "tool_use",
			Raw: json.RawMessage(`{"type":"assistant","i":2}`),
			// Usage == nil — must not crash; spec #353 allows it.
		},
		{
			Kind: "tool_use",
			Raw:  json.RawMessage(`{"type":"tool_use"}`),
		},
		{
			Kind: "assistant", StopReason: "end_turn", EndOfTurn: true,
			Raw:   json.RawMessage(`{"type":"assistant","i":3}`),
			Usage: &jsonl.UsageBlock{InputTokens: 20, OutputTokens: 3, CacheCreationInputTokens: 200, CacheReadInputTokens: 0},
		},
	}
	for _, ev := range events {
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
		ev := jsonl.Event{Kind: k, Raw: json.RawMessage(`{}`)}
		if k == "assistant" && i == len(kinds)-1 {
			ev.StopReason = "end_turn"
			ev.EndOfTurn = true
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
		ev := jsonl.Event{Kind: "assistant", StopReason: sr, Raw: json.RawMessage(`{}`)}
		if sr == "end_turn" {
			ev.EndOfTurn = true
		}
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
		if err := em.Emit(jsonl.Event{Kind: k, Raw: json.RawMessage(`{}`)}); err != nil {
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
	ev := jsonl.Event{
		Kind: "assistant", StopReason: "end_turn", EndOfTurn: true,
		Raw: json.RawMessage(`{"type":"assistant"}`),
	}
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
	if err := em.Emit(jsonl.Event{Kind: "assistant", StopReason: "tool_use", Raw: json.RawMessage(`{"type":"assistant"}`)}); err != nil {
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
	if err := em.Emit(jsonl.Event{Kind: "assistant", StopReason: "tool_use", Raw: json.RawMessage(`{"type":"assistant"}`)}); err != nil {
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
	if err := em.Emit(jsonl.Event{Kind: "assistant", StopReason: "tool_use", Raw: json.RawMessage(`{}`)}); err != nil {
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
	if err := em.Emit(jsonl.Event{Kind: "assistant", StopReason: "end_turn", EndOfTurn: true, Raw: json.RawMessage(`{}`)}); err != nil {
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
	if err := em.Emit(jsonl.Event{Kind: "assistant", Raw: json.RawMessage(`{"late":"event"}`)}); err != nil {
		t.Errorf("Emit after Close: %v (want nil no-op)", err)
	}
	if buf.Len() != closedLen {
		t.Errorf("Emit after Close wrote bytes: len before=%d, after=%d", closedLen, buf.Len())
	}
}

// failingWriter returns err on the first Write, then nothing.
type failingWriter struct {
	err    error
	writes int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	return 0, f.err
}

func TestEmit_WriteErrorIsSticky(t *testing.T) {
	t.Parallel()
	w := &failingWriter{err: errors.New("boom")}
	em, err := New(Config{Writer: w, SessionID: "11111111-1111-4111-8111-111111111111"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := em.Emit(jsonl.Event{Kind: "assistant", Raw: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("first Emit: want error, got nil")
	}
	// Second Emit must be a silent no-op — sticky writeErr short-circuits.
	if err := em.Emit(jsonl.Event{Kind: "assistant", Raw: json.RawMessage(`{}`)}); err != nil {
		t.Errorf("second Emit: %v (want nil no-op)", err)
	}
	if w.writes != 1 {
		t.Errorf("failing writer was called %d times, want 1 (sticky short-circuit)", w.writes)
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
	em, err := New(Config{Writer: buf, SessionID: "11111111-1111-4111-8111-111111111111", Now: now})
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
	em, err := New(Config{Writer: buf, SessionID: id})
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

// TestCapturedFixture_ByteEquivalence replays the non-result lines from
// testdata/captured_run.jsonl through the emitter, calls Close, and asserts:
//
//   (a) each replayed non-result line is byte-equivalent to its fixture
//       counterpart;
//   (b) the trailer pyry composes matches the fixture's result line on the
//       field set defined by this spec (type, subtype, is_error, num_turns,
//       stop_reason, terminal_reason, and the four usage int counters).
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
	// Split on '\n' but preserve trailing '\r' if any (jsonl.Reader does).
	allLines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(allLines) < 2 {
		t.Fatalf("fixture has %d lines, want >=2 (events + trailer)", len(allLines))
	}
	nonResult := allLines[:len(allLines)-1]
	fixtureResultLine := allLines[len(allLines)-1]

	// Drive the fixture's non-result lines through jsonl.Reader so we feed
	// the emitter the same Event shapes the watcher does in production.
	src := bytes.NewReader([]byte(strings.Join(nonResult, "\n") + "\n"))
	rdr := jsonl.NewReader(src, jsonl.Config{})

	buf := &bytes.Buffer{}
	em, err := New(Config{
		Writer:    buf,
		SessionID: "11111111-1111-4111-8111-111111111111",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for {
		ev, err := rdr.Next()
		if err != nil {
			if errors.Is(err, jsonl.ErrLineTooLarge) {
				t.Fatalf("fixture line too large")
			}
			break // io.EOF or unexpected; either way the loop is done
		}
		if err := em.Emit(ev); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(out) != len(nonResult)+1 {
		t.Fatalf("pyry stdout has %d lines, want %d (non-result + trailer)", len(out), len(nonResult)+1)
	}
	// (a) non-result lines byte-equivalent.
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
