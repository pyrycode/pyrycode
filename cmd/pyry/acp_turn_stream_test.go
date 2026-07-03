package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/acpbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const testStreamSessionID = "sess-123"

// streamHarness wires an acpTurnStream to a real acp.Transport whose writer is
// an in-memory buffer, so a scripted turnevent sequence can be driven through
// Handle and the emitted notification frames decoded off the wire. The stderr
// buffer captures the stream's own diagnostics (the Stall surfacing).
type streamHarness struct {
	stream *acpTurnStream
	wire   *bytes.Buffer // transport writer: the session/update notification stream
	stderr *bytes.Buffer // the stream's slog output (stderr surface)
}

func newStreamHarness(t *testing.T, onTurnEnd func(string)) *streamHarness {
	t.Helper()
	var wire, stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := acp.New(strings.NewReader(""), &wire, logger)
	stream := newACPTurnStream(tr, testStreamSessionID, onTurnEnd, logger)
	return &streamHarness{stream: stream, wire: &wire, stderr: &stderr}
}

// frames decodes the transport writer output into JSON-RPC frames (one per line).
func (h *streamHarness) frames(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(h.wire.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("frame not valid JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// assertNotification asserts frame is a well-formed session/update notification
// (correct method, correct sessionId, no id/result/error) and returns its
// nested update object.
func assertNotification(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	for _, k := range []string{"id", "result", "error"} {
		if _, ok := frame[k]; ok {
			t.Errorf("session/update carries %q: %v", k, frame)
		}
	}
	if frame["method"] != acpbridge.MethodSessionUpdate {
		t.Errorf("method = %v, want %v", frame["method"], acpbridge.MethodSessionUpdate)
	}
	params, ok := frame["params"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no params object: %v", frame)
	}
	if params["sessionId"] != testStreamSessionID {
		t.Errorf("sessionId = %v, want %v", params["sessionId"], testStreamSessionID)
	}
	update, ok := params["update"].(map[string]any)
	if !ok {
		t.Fatalf("params has no update object: %v", params)
	}
	return update
}

func discriminant(t *testing.T, update map[string]any) string {
	t.Helper()
	su, _ := update["sessionUpdate"].(string)
	return su
}

// AC-1: a scripted content sequence produces one session/update per event, each
// with the expected sessionUpdate discriminant and content, in order.
func TestACPTurnStream_EmitsSessionUpdatePerEvent(t *testing.T) {
	t.Parallel()
	h := newStreamHarness(t, nil)

	h.stream.Handle(turnevent.TextChunk{MessageID: "m1", Text: "hi"})
	h.stream.Handle(turnevent.ThoughtChunk{MessageID: "m1", Text: "hmm"})
	h.stream.Handle(turnevent.ToolStart{ToolCallID: "t1", Title: "Read", Kind: turnevent.ToolKindRead})
	h.stream.Handle(turnevent.ToolUpdate{ToolCallID: "t1", Status: turnevent.ToolStatusCompleted})

	frames := h.frames(t)
	wantDiscriminants := []string{
		acpbridge.SessionUpdateAgentMessageChunk,
		acpbridge.SessionUpdateAgentThoughtChunk,
		acpbridge.SessionUpdateToolCall,
		acpbridge.SessionUpdateToolCallUpdate,
	}
	if len(frames) != len(wantDiscriminants) {
		t.Fatalf("emitted %d frames, want %d: %v", len(frames), len(wantDiscriminants), frames)
	}
	for i, frame := range frames {
		update := assertNotification(t, frame)
		if got := discriminant(t, update); got != wantDiscriminants[i] {
			t.Errorf("frame %d sessionUpdate = %q, want %q", i, got, wantDiscriminants[i])
		}
	}

	// Spot-check the text content survives the mapping intact.
	firstContent, _ := assertNotification(t, frames[0])["content"].(map[string]any)
	if firstContent["text"] != "hi" {
		t.Errorf("agent_message_chunk text = %v, want hi", firstContent["text"])
	}
}

// AC-2: TextChunks sharing a message id stream as separate agent_message_chunk
// notifications in arrival order — no coalescing — with a tool_call interleaved
// and a fresh-message chunk after it.
func TestACPTurnStream_GroupsChunksByMessageID(t *testing.T) {
	t.Parallel()
	h := newStreamHarness(t, nil)

	h.stream.Handle(turnevent.TextChunk{MessageID: "m1", Text: "Hello "})
	h.stream.Handle(turnevent.TextChunk{MessageID: "m1", Text: "world"})
	h.stream.Handle(turnevent.ToolStart{ToolCallID: "t1", Title: "Read", Kind: turnevent.ToolKindRead})
	h.stream.Handle(turnevent.TextChunk{MessageID: "m2", Text: "!"})

	frames := h.frames(t)
	if len(frames) != 4 {
		t.Fatalf("emitted %d frames, want 4 (no coalescing): %v", len(frames), frames)
	}

	type shape struct {
		discriminant string
		text         string // "" when not a content chunk
	}
	want := []shape{
		{acpbridge.SessionUpdateAgentMessageChunk, "Hello "},
		{acpbridge.SessionUpdateAgentMessageChunk, "world"},
		{acpbridge.SessionUpdateToolCall, ""},
		{acpbridge.SessionUpdateAgentMessageChunk, "!"},
	}
	for i, frame := range frames {
		update := assertNotification(t, frame)
		if got := discriminant(t, update); got != want[i].discriminant {
			t.Fatalf("frame %d discriminant = %q, want %q", i, got, want[i].discriminant)
		}
		if want[i].text != "" {
			content, _ := update["content"].(map[string]any)
			if content["text"] != want[i].text {
				t.Errorf("frame %d text = %v, want %q", i, content["text"], want[i].text)
			}
		}
	}
}

// AC-3: TurnEnd emits no session/update and signals onTurnEnd exactly once with
// the ACP stopReason.
func TestACPTurnStream_TurnEndSignalsAndEmitsNothing(t *testing.T) {
	t.Parallel()
	var reasons []string
	h := newStreamHarness(t, func(r string) { reasons = append(reasons, r) })

	h.stream.Handle(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	if got := h.frames(t); len(got) != 0 {
		t.Errorf("TurnEnd emitted %d frames, want 0: %v", len(got), got)
	}
	if len(reasons) != 1 || reasons[0] != string(turnevent.TurnEndReasonEndTurn) {
		t.Errorf("onTurnEnd got %v, want exactly [%q]", reasons, turnevent.TurnEndReasonEndTurn)
	}
}

// AC-3 (sub-case): a nil onTurnEnd resolver does not panic and emits no frame.
func TestACPTurnStream_TurnEndNilResolverNoPanic(t *testing.T) {
	t.Parallel()
	h := newStreamHarness(t, nil)

	h.stream.Handle(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled})

	if got := h.frames(t); len(got) != 0 {
		t.Errorf("TurnEnd (nil resolver) emitted %d frames, want 0: %v", len(got), got)
	}
}

// AC-4: Stall emits no session/update frame and surfaces on stderr instead.
func TestACPTurnStream_StallDropsToStderr(t *testing.T) {
	t.Parallel()
	h := newStreamHarness(t, nil)

	h.stream.Handle(turnevent.Stall{})

	if got := h.frames(t); len(got) != 0 {
		t.Errorf("Stall emitted %d frames, want 0: %v", len(got), got)
	}
	if !strings.Contains(h.stderr.String(), "acp_turn.stall") {
		t.Errorf("Stall not surfaced on stderr; stderr = %q", h.stderr.String())
	}
}

// Defensive: a nil event reaches neither TurnEnd/Stall nor an emit-able variant,
// so MapUpdate reports ok==false — no frame, no panic.
func TestACPTurnStream_NilEventDropsNoFrame(t *testing.T) {
	t.Parallel()
	h := newStreamHarness(t, nil)

	h.stream.Handle(nil)

	if got := h.frames(t); len(got) != 0 {
		t.Errorf("nil event emitted %d frames, want 0: %v", len(got), got)
	}
}
