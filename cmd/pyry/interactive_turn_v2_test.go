package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// recordedPush captures one (*interactiveTurnEmitterV2).emit -> Push attempt:
// the addressed conn and the envelope it carried. Captured in call order
// (Handle is serial on a single goroutine, so no synchronisation is needed).
type recordedPush struct {
	connID string
	env    protocol.Envelope
}

// fakeInteractiveBcast is a test double for the interactiveBroadcaster surface
// (ActiveConns + Push). It returns a scripted sequence of open-conn snapshots
// (the last entry is reused once the sequence is exhausted, modelling a steady
// set), records every Push attempt, and can inject a per-conn Push error. No
// mutex/channel: the emitter spawns no goroutine, so every call lands on the
// test goroutine in Handle order.
type fakeInteractiveBcast struct {
	snapshots [][]relay.ActiveConn // one entry consumed per ActiveConns call
	callIdx   int
	pushErr   map[string]error // connID -> error Push returns for it

	pushes []recordedPush
}

func (f *fakeInteractiveBcast) ActiveConns(ctx context.Context) []relay.ActiveConn {
	if len(f.snapshots) == 0 {
		return nil
	}
	idx := f.callIdx
	if idx >= len(f.snapshots) {
		idx = len(f.snapshots) - 1 // steady-state: reuse the last snapshot
	}
	f.callIdx++
	out := make([]relay.ActiveConn, len(f.snapshots[idx]))
	copy(out, f.snapshots[idx])
	return out
}

func (f *fakeInteractiveBcast) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	err := f.pushErr[connID]
	// Record the attempt regardless of error so a test can prove the loop
	// continued past a failing conn.
	f.pushes = append(f.pushes, recordedPush{connID: connID, env: env})
	return err
}

// --- decode helpers -------------------------------------------------------

func pushTypes(pushes []recordedPush) []string {
	out := make([]string, len(pushes))
	for i, p := range pushes {
		out[i] = p.env.Type
	}
	return out
}

func pushesFor(pushes []recordedPush, connID string) []recordedPush {
	var out []recordedPush
	for _, p := range pushes {
		if p.connID == connID {
			out = append(out, p)
		}
	}
	return out
}

func turnStateValues(t *testing.T, pushes []recordedPush) []string {
	t.Helper()
	var out []string
	for _, p := range pushes {
		if p.env.Type != protocol.TypeTurnState {
			continue
		}
		var ts protocol.TurnStatePayload
		if err := json.Unmarshal(p.env.Payload, &ts); err != nil {
			t.Fatalf("decode turn_state payload: %v", err)
		}
		out = append(out, ts.State)
	}
	return out
}

func assistantDeltas(t *testing.T, pushes []recordedPush) []protocol.AssistantDeltaPayload {
	t.Helper()
	var out []protocol.AssistantDeltaPayload
	for _, p := range pushes {
		if p.env.Type != protocol.TypeAssistantDelta {
			continue
		}
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(p.env.Payload, &d); err != nil {
			t.Fatalf("decode assistant_delta payload: %v", err)
		}
		out = append(out, d)
	}
	return out
}

// --- tests ----------------------------------------------------------------

// AC#2: turn_state transitions are derived statefully (thinking on a thought,
// responding on first content, idle on turn end) in the right order, and the
// per-kind content envelopes interleave as specified.
func TestInteractiveTurnEmitterV2_TransitionOrder(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "Read"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	wantTypes := []string{
		protocol.TypeTurnState,      // thinking
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // hello
		protocol.TypeToolUse,        // Read
		protocol.TypeTurnEnd,        // end_turn
		protocol.TypeTurnState,      // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("envelope type order:\n got %v\nwant %v", got, wantTypes)
	}
	wantStates := []string{"thinking", "responding", "idle"}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, wantStates) {
		t.Fatalf("turn_state order: got %v, want %v", got, wantStates)
	}
}

// AC#2: state-change de-dup — an interleave of thought/text re-emits each
// transition but never a duplicate same-state envelope.
func TestInteractiveTurnEmitterV2_InterleaveDeDup(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "t1"},
		turnevent.TextChunk{Text: "x1"},
		turnevent.ThoughtChunk{Text: "t2"},
		turnevent.TextChunk{Text: "x2"},
	} {
		e.Handle(context.Background(), ev)
	}

	wantStates := []string{"thinking", "responding", "thinking", "responding"}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, wantStates) {
		t.Fatalf("interleave turn_state: got %v, want %v", got, wantStates)
	}
}

// AC#1: the per-turn seq resets at each turn boundary; turn ids are fresh
// (distinct, canonical UUIDv4) per turn.
func TestInteractiveTurnEmitterV2_PerTurnSeqReset(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		// turn A
		turnevent.TextChunk{Text: "a1"},
		turnevent.TextChunk{Text: "a2"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
		// turn B
		turnevent.TextChunk{Text: "b1"},
		turnevent.TextChunk{Text: "b2"},
	} {
		e.Handle(context.Background(), ev)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 4 {
		t.Fatalf("want 4 assistant_delta, got %d", len(deltas))
	}
	wantSeq := []int{0, 1, 0, 1}
	for i, d := range deltas {
		if d.Seq != wantSeq[i] {
			t.Fatalf("delta[%d].Seq = %d, want %d", i, d.Seq, wantSeq[i])
		}
	}
	turnA, turnB := deltas[0].TurnID, deltas[2].TurnID
	if deltas[1].TurnID != turnA {
		t.Fatalf("turn A deltas have different turn ids: %q vs %q", turnA, deltas[1].TurnID)
	}
	if deltas[3].TurnID != turnB {
		t.Fatalf("turn B deltas have different turn ids: %q vs %q", turnB, deltas[3].TurnID)
	}
	if turnA == turnB {
		t.Fatalf("turn A and turn B share a turn id %q; want distinct", turnA)
	}
	if !conversations.ValidID(turnA) || !conversations.ValidID(turnB) {
		t.Fatalf("turn ids not canonical UUIDv4: %q, %q", turnA, turnB)
	}
}

// AC#3: the envelope-ID counter is session-monotonic with no reset across the
// turn boundary.
func TestInteractiveTurnEmitterV2_MonotonicEnvIDAcrossTurns(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{Text: "a1"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
		turnevent.TextChunk{Text: "b1"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	if len(bcast.pushes) == 0 {
		t.Fatal("no envelopes pushed")
	}
	var prev uint64
	for i, p := range bcast.pushes {
		if p.env.ID <= prev {
			t.Fatalf("env.ID not strictly increasing at push %d: %d after %d", i, p.env.ID, prev)
		}
		prev = p.env.ID
	}
}

// AC#4 + § Security: the structured stream reaches only interactive-granted
// conns; a non-interactive conn in the same snapshot is never pushed to.
func TestInteractiveTurnEmitterV2_FanOutOnlyInteractive(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "Read"},
		turnevent.ToolUpdate{ToolCallID: "t1", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "ok"}},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	if len(pushesFor(bcast.pushes, "a")) == 0 {
		t.Fatal("interactive conn a received no envelopes")
	}
	if got := len(pushesFor(bcast.pushes, "b")); got != 0 {
		t.Fatalf("non-interactive conn b received %d envelopes; want 0", got)
	}
	for _, p := range bcast.pushes {
		if p.connID != "a" {
			t.Fatalf("envelope %q pushed to unexpected conn %q", p.env.Type, p.connID)
		}
	}
}

// AC#4: a conn that joins mid-turn is included in subsequent fan-outs only.
func TestInteractiveTurnEmitterV2_MidTurnJoin(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	// ActiveConns call #1 (thinking emit) sees only a; calls #2+ (responding,
	// delta) see a and b. The thought emits exactly one envelope, so b joins
	// strictly after the first event.
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{
		{{ConnID: "a", Interactive: true}},
		{{ConnID: "a", Interactive: true}, {ConnID: "b", Interactive: true}},
	}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ThoughtChunk{Text: "reasoning"}) // emit#1: [a]
	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})        // emit#2,#3: [a,b]

	// a saw everything: thinking, responding, assistant_delta.
	wantA := []string{protocol.TypeTurnState, protocol.TypeTurnState, protocol.TypeAssistantDelta}
	if got := pushTypes(pushesFor(bcast.pushes, "a")); !slices.Equal(got, wantA) {
		t.Fatalf("conn a envelopes: got %v, want %v", got, wantA)
	}
	// b joined for the second event: responding + assistant_delta, never the
	// first event's thinking.
	wantB := []string{protocol.TypeTurnState, protocol.TypeAssistantDelta}
	if got := pushTypes(pushesFor(bcast.pushes, "b")); !slices.Equal(got, wantB) {
		t.Fatalf("conn b envelopes: got %v, want %v", got, wantB)
	}
	for _, p := range pushesFor(bcast.pushes, "b") {
		if p.env.Type == protocol.TypeTurnState {
			var ts protocol.TurnStatePayload
			if err := json.Unmarshal(p.env.Payload, &ts); err != nil {
				t.Fatalf("decode b turn_state: %v", err)
			}
			if ts.State == "thinking" {
				t.Fatal("conn b received the pre-join thinking transition")
			}
		}
	}
}

// AC#4: a per-conn Push error is non-fatal — the turn continues for the other
// conns.
func TestInteractiveTurnEmitterV2_PushErrorDoesNotAbortTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "a", Interactive: true},
			{ConnID: "b", Interactive: true},
			{ConnID: "c", Interactive: true},
		}},
		pushErr: map[string]error{"b": relay.ErrConnNotFound},
	}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	// The failing middle conn must not stop a or c (which is pushed after b)
	// from receiving every envelope.
	a := pushTypes(pushesFor(bcast.pushes, "a"))
	c := pushTypes(pushesFor(bcast.pushes, "c"))
	if len(a) == 0 {
		t.Fatal("conn a received no envelopes")
	}
	if !slices.Equal(a, c) {
		t.Fatalf("conn c (after failing b) diverged from a:\n a=%v\n c=%v", a, c)
	}
}

// AC#5: application output (thought text, assistant text, tool title/input,
// tool result) is NEVER logged at any level, and thought text is never
// forwarded on the wire.
func TestInteractiveTurnEmitterV2_NoAppOutputLogLeak(t *testing.T) {
	t.Parallel()
	const (
		secretThought   = "SECRETTHOUGHTZZZ"
		secretAssistant = "SECRETASSISTANTZZZ"
		secretToolTitle = "SECRETTOOLTITLEZZZ"
		secretToolInput = "SECRETINPUTZZZ"
		secretToolReslt = "SECRETRESULTZZZ"
	)

	var buf bytes.Buffer // synchronous single-goroutine capture: bytes.Buffer is safe here
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cur := &stubCursor{}
	cur.set(testConvID)
	// Push fails on the one conn so the push_err DEBUG branch (which logs the
	// transport sentinel err) fires for every envelope — exercising the most
	// log-heavy path.
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}},
		pushErr:   map[string]error{"a": relay.ErrConnNotFound},
	}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: secretThought},
		turnevent.TextChunk{Text: secretAssistant},
		turnevent.ToolStart{ToolCallID: "t1", Title: secretToolTitle, RawInput: json.RawMessage(`{"query":"` + secretToolInput + `"}`)},
		turnevent.ToolUpdate{ToolCallID: "t1", Status: turnevent.ToolStatusFailed, Content: turnevent.TextContent{Text: secretToolReslt}},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected DEBUG push-error logs; got none (test would not prove the no-leak property)")
	}
	for _, secret := range []string{secretThought, secretAssistant, secretToolTitle, secretToolInput, secretToolReslt} {
		if strings.Contains(logs, secret) {
			t.Fatalf("application output %q leaked into logs:\n%s", secret, logs)
		}
	}

	// Thought text must never reach the wire either (MapEvent drops ThoughtChunk).
	for _, p := range bcast.pushes {
		if bytes.Contains(p.env.Payload, []byte(secretThought)) {
			t.Fatalf("thought text leaked into a %q envelope payload", p.env.Type)
		}
	}
}

// AC#1: an empty cursor drops the event with no push (mirrors #589).
func TestInteractiveTurnEmitterV2_DropsWhenCursorEmpty(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{} // empty cursor
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})

	if len(bcast.pushes) != 0 {
		t.Fatalf("empty cursor pushed %d envelopes; want 0", len(bcast.pushes))
	}
}

// A TurnEnd observed while no turn is open is dropped (no turn_end, no idle).
func TestInteractiveTurnEmitterV2_TurnEndOutsideTurnDropped(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	if len(bcast.pushes) != 0 {
		t.Fatalf("turn_end outside a turn pushed %d envelopes; want 0", len(bcast.pushes))
	}
}
