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
	"github.com/pyrycode/pyrycode/internal/eventring"
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

func ringEventTypes(evs []eventring.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func ringEventIDs(evs []eventring.Event) []uint64 {
	out := make([]uint64, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

// pushEventIDs extracts the durable wire event id (env.EventID) from each
// recorded push, failing if any is nil — every interactive frame must carry one.
func pushEventIDs(t *testing.T, pushes []recordedPush) []uint64 {
	t.Helper()
	out := make([]uint64, len(pushes))
	for i, p := range pushes {
		if p.env.EventID == nil {
			t.Fatalf("push %d (%s) has nil EventID; want a durable id on the wire", i, p.env.Type)
		}
		out[i] = *p.env.EventID
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
		// turn A — distinct MessageIDs so each TextChunk is its own delta: the
		// a2 boundary flushes a1 (seq 0), the trailing TurnEnd flushes a2 (seq 1).
		turnevent.TextChunk{MessageID: "ma1", Text: "a1"},
		turnevent.TextChunk{MessageID: "ma2", Text: "a2"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
		// turn B — likewise; the trailing TurnEnd flushes b2 so both deltas emit.
		turnevent.TextChunk{MessageID: "mb1", Text: "b1"},
		turnevent.TextChunk{MessageID: "mb2", Text: "b2"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
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
	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})        // buffers; turn_state responding emit#2: [a,b]
	e.flushDelta(context.Background())                                        // coalesced delta emit#3: [a,b]

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

// AC#1: a stall_detected mapped to turnevent.Stall fans out as a stall envelope
// to interactive-capable conns only (the capability gate), carrying the cursor's
// conversation_id.
func TestInteractiveTurnEmitterV2_StallFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.Stall{})

	if got := len(bcast.pushes); got != 1 {
		t.Fatalf("stall pushed %d envelopes; want exactly 1 (interactive conn only)", got)
	}
	p := bcast.pushes[0]
	if p.connID != "a" {
		t.Fatalf("stall pushed to conn %q; want interactive conn %q", p.connID, "a")
	}
	if p.env.Type != protocol.TypeStall {
		t.Fatalf("envelope type: got %q, want %q", p.env.Type, protocol.TypeStall)
	}
	var sp protocol.StallPayload
	if err := json.Unmarshal(p.env.Payload, &sp); err != nil {
		t.Fatalf("decode stall payload: %v", err)
	}
	if sp.ConversationID != testConvID {
		t.Fatalf("stall conversation_id: got %q, want %q", sp.ConversationID, testConvID)
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatal("non-interactive conn b received the stall")
	}
}

// AC#1: a stall mutates no turn lifecycle — it emits no turn_state and leaves
// inTurn/currentState untouched, so the next content opens a fresh turn as if
// the stall never happened.
func TestInteractiveTurnEmitterV2_StallNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// A bare stall before any turn: only the stall, no turn_state / delta.
	e.Handle(context.Background(), turnevent.Stall{})
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeStall}) {
		t.Fatalf("bare stall envelopes: got %v, want [%s]", got, protocol.TypeStall)
	}

	// The next content opens a fresh turn: first subsequent envelope is
	// turn_state: responding (the stall left inTurn/currentState untouched).
	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background()) // emit the coalesced delta (models a flush)
	wantTypes := []string{
		protocol.TypeStall,
		protocol.TypeTurnState,      // responding — fresh turn opens after the stall
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-stall envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after stall: got %v, want [responding]", got)
	}
}

// AC#1: a stall mid-turn rides through without disturbing the open turn — seq
// keeps advancing, the turn id is unchanged, and no extra turn_state is emitted
// around the stall.
func TestInteractiveTurnEmitterV2_StallMidTurnDoesNotDisturbOpenTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{Text: "a1"}, // opens turn: responding, buffers a1
		turnevent.Stall{},               // stall mid-turn flushes a1 (seq 0) first
		turnevent.TextChunk{Text: "a2"}, // buffers a2 (same id), seq 1 on flush
	} {
		e.Handle(context.Background(), ev)
	}
	e.flushDelta(context.Background()) // flush the trailing a2 delta

	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // a1 (flushed by the stall)
		protocol.TypeStall,          // stall, no surrounding turn_state
		protocol.TypeAssistantDelta, // a2
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn stall envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("stall disrupted seq: got %d,%d want 0,1", deltas[0].Seq, deltas[1].Seq)
	}
	if deltas[0].TurnID != deltas[1].TurnID {
		t.Fatalf("stall split the turn: %q vs %q", deltas[0].TurnID, deltas[1].TurnID)
	}
}

// AC#1: an empty cursor drops the stall with no push (the existing Handle gate
// covers the stall for free).
func TestInteractiveTurnEmitterV2_StallDroppedWhenCursorEmpty(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{} // empty cursor
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.Stall{})

	if len(bcast.pushes) != 0 {
		t.Fatalf("empty cursor pushed %d stall envelopes; want 0", len(bcast.pushes))
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

// --- #609 delta coalescing ---------------------------------------------------

// AC#1: consecutive TextChunks with the SAME MessageID concatenate in arrival
// order into ONE assistant_delta (per-JSONL-message batching, not per-line),
// flushed before turn_end at the turn boundary.
func TestInteractiveTurnEmitterV2_SameIDCoalesce(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{MessageID: "m1", Text: "Hel"},
		turnevent.TextChunk{MessageID: "m1", Text: "lo"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 1 {
		t.Fatalf("same-id chunks: got %d assistant_delta, want 1 (coalesced)", len(deltas))
	}
	if deltas[0].Text != "Hello" {
		t.Fatalf("coalesced text: got %q, want %q (concatenated in arrival order)", deltas[0].Text, "Hello")
	}
	if deltas[0].Seq != 0 {
		t.Fatalf("coalesced delta seq: got %d, want 0", deltas[0].Seq)
	}
	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // "Hello" (coalesced)
		protocol.TypeTurnEnd,
		protocol.TypeTurnState, // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("envelope order:\n got %v\nwant %v", got, wantTypes)
	}
}

// AC#1: a TextChunk with a NEW MessageID flushes the buffered delta first, then
// starts a fresh buffer — two deltas, each its own message, seq 0 then 1.
func TestInteractiveTurnEmitterV2_NewIDBoundaryFlush(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{MessageID: "m1", Text: "A"},
		turnevent.TextChunk{MessageID: "m2", Text: "B"}, // new id flushes "A" first
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("two message ids: got %d assistant_delta, want 2", len(deltas))
	}
	if deltas[0].Text != "A" || deltas[0].Seq != 0 {
		t.Fatalf("delta[0]: got {%q, seq %d}, want {A, 0}", deltas[0].Text, deltas[0].Seq)
	}
	if deltas[1].Text != "B" || deltas[1].Seq != 1 {
		t.Fatalf("delta[1]: got {%q, seq %d}, want {B, 1}", deltas[1].Text, deltas[1].Seq)
	}
	if deltas[0].TurnID != deltas[1].TurnID {
		t.Fatalf("both deltas are one turn but carry different turn ids: %q vs %q", deltas[0].TurnID, deltas[1].TurnID)
	}
}

// AC#2: a timer flush mid-message (modelled by a direct flushDelta call — the
// spec's deterministic stand-in for the ~250ms fire) emits the accumulated
// prefix; the same message split across the window keeps one rising seq.
func TestInteractiveTurnEmitterV2_TimerFlushMidMessage(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "A"})
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "B"})
	e.flushDelta(context.Background()) // ~250ms timer fires mid-message
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "C"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	deltas := assistantDeltas(t, bcast.pushes)
	want := []struct {
		text string
		seq  int
	}{{"AB", 0}, {"C", 1}}
	if len(deltas) != len(want) {
		t.Fatalf("got %d assistant_delta, want %d", len(deltas), len(want))
	}
	for i, w := range want {
		if deltas[i].Text != w.text || deltas[i].Seq != w.seq {
			t.Fatalf("delta[%d]: got {%q, seq %d}, want {%q, %d}", i, deltas[i].Text, deltas[i].Seq, w.text, w.seq)
		}
	}
}

// AC#3: a non-empty buffer is flushed BEFORE any interleaved non-text envelope
// (tool_use / turn_state{thinking} / stall) so wire ordering is preserved.
func TestInteractiveTurnEmitterV2_FlushBeforeNonText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		trailing  turnevent.Event
		wantTypes []string
	}{
		{
			name:     "before tool_use",
			trailing: turnevent.ToolStart{ToolCallID: "t1", Title: "Read"},
			wantTypes: []string{
				protocol.TypeTurnState,      // responding
				protocol.TypeAssistantDelta, // "A" — flushed before the tool
				protocol.TypeToolUse,
			},
		},
		{
			name:     "before thinking",
			trailing: turnevent.ThoughtChunk{Text: "hmm"},
			wantTypes: []string{
				protocol.TypeTurnState,      // responding
				protocol.TypeAssistantDelta, // "A" — flushed before the thinking transition
				protocol.TypeTurnState,      // thinking
			},
		},
		{
			name:     "before stall",
			trailing: turnevent.Stall{},
			wantTypes: []string{
				protocol.TypeTurnState,      // responding
				protocol.TypeAssistantDelta, // "A" — flushed before the stall
				protocol.TypeStall,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cur := &stubCursor{}
			cur.set(testConvID)
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

			e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "A"})
			e.Handle(context.Background(), tt.trailing)

			if got := pushTypes(bcast.pushes); !slices.Equal(got, tt.wantTypes) {
				t.Fatalf("%s envelope order:\n got %v\nwant %v", tt.name, got, tt.wantTypes)
			}
			deltas := assistantDeltas(t, bcast.pushes)
			if len(deltas) != 1 || deltas[0].Text != "A" {
				t.Fatalf("%s: want one assistant_delta {A} before the non-text envelope, got %+v", tt.name, deltas)
			}
		})
	}
}

// AC#3: the buffer is flushed at the turn boundary — the delta precedes turn_end
// and the idle turn_state.
func TestInteractiveTurnEmitterV2_FlushAtTurnBoundary(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{MessageID: "m1", Text: "A"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // "A" — flushed before turn_end, before idle
		protocol.TypeTurnEnd,
		protocol.TypeTurnState, // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("turn-boundary envelope order:\n got %v\nwant %v", got, wantTypes)
	}
}

// AC#3: seq advances ONCE per emitted coalesced delta, never once per buffered
// TextChunk — three same-id chunks yield one delta at seq 0.
func TestInteractiveTurnEmitterV2_OneSeqPerCoalescedDelta(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{MessageID: "m1", Text: "x"},
		turnevent.TextChunk{MessageID: "m1", Text: "y"},
		turnevent.TextChunk{MessageID: "m1", Text: "z"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 1 {
		t.Fatalf("three same-id chunks: got %d assistant_delta, want 1", len(deltas))
	}
	if deltas[0].Seq != 0 {
		t.Fatalf("seq advanced per buffered chunk: got %d, want 0 (one seq per coalesced delta)", deltas[0].Seq)
	}
	if deltas[0].Text != "xyz" {
		t.Fatalf("coalesced text: got %q, want %q", deltas[0].Text, "xyz")
	}
}

// --- #646 durable event ring -------------------------------------------------

// AC-1: every fanned-out event is recorded in the durable per-conversation ring
// with strictly increasing ids 1..N, in the same order the wire saw them.
func TestInteractiveTurnEmitterV2_RingRecordsEmittedEvents(t *testing.T) {
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

	got, gap := e.ring.After(testConvID, 0)
	if gap {
		t.Fatal("ring reported a gap for a fresh query")
	}
	wantTypes := []string{
		protocol.TypeTurnState,      // thinking
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // hello (flushed before tool_use)
		protocol.TypeToolUse,        // Read
		protocol.TypeTurnEnd,        // end_turn
		protocol.TypeTurnState,      // idle
	}
	if rt := ringEventTypes(got); !slices.Equal(rt, wantTypes) {
		t.Fatalf("ring event types:\n got %v\nwant %v", rt, wantTypes)
	}
	for i, id := range ringEventIDs(got) {
		if id != uint64(i+1) {
			t.Fatalf("ring ids not 1..N: got %v", ringEventIDs(got))
		}
	}
}

// AC-1: the durable id is assigned ONCE per logical event, before the per-conn
// fan-out — two interactive conns yield one ring entry per logical event while
// the broadcaster records one push per conn per envelope.
func TestInteractiveTurnEmitterV2_RingIDIsPerEventNotPerConn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: true},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "Read"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	got, _ := e.ring.After(testConvID, 0)
	const wantEvents = 6 // thinking, responding, delta, tool_use, turn_end, idle
	if len(got) != wantEvents {
		t.Fatalf("ring recorded %d events, want %d (one per logical event, not per conn)", len(got), wantEvents)
	}
	for i, id := range ringEventIDs(got) {
		if id != uint64(i+1) {
			t.Fatalf("ring ids not 1..N: got %v", ringEventIDs(got))
		}
	}
	if len(bcast.pushes) != wantEvents*2 {
		t.Fatalf("broadcaster recorded %d pushes, want %d (2 conns x %d envelopes)", len(bcast.pushes), wantEvents*2, wantEvents)
	}
}

// AC-1: events are appended to the ring even with zero interactive conns — the
// ring is the replay source for phones that are absent now and reconnect later.
func TestInteractiveTurnEmitterV2_RingAppendsWithNoInteractiveConns(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{}}} // a snapshot with no conns
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{Text: "hello"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	if len(bcast.pushes) != 0 {
		t.Fatalf("no interactive conns but %d pushes recorded", len(bcast.pushes))
	}
	got, _ := e.ring.After(testConvID, 0)
	if len(got) == 0 {
		t.Fatal("ring is empty though events were emitted to an absent audience")
	}
}

// AC-1: an empty cursor drops the event before emit, so nothing is recorded in
// the ring.
func TestInteractiveTurnEmitterV2_RingEmptyOnEmptyCursor(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{} // empty cursor
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})

	got, gap := e.ring.After(testConvID, 0)
	if gap || len(got) != 0 {
		t.Fatalf("empty cursor recorded events: %v (gap=%v)", ringEventTypes(got), gap)
	}
}

// --- #649 durable event id on the wire --------------------------------------

// AC-1: every fanned-out envelope carries its durable per-conversation event id
// on the wire (env.EventID), and that id equals the ring id recorded for the
// same logical event, in the same order.
func TestInteractiveTurnEmitterV2_WireCarriesDurableEventID(t *testing.T) {
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

	ring, _ := e.ring.After(testConvID, 0)
	wire := pushEventIDs(t, bcast.pushes)
	if want := ringEventIDs(ring); !slices.Equal(wire, want) {
		t.Fatalf("wire event ids:\n got %v\nwant %v (ring ids)", wire, want)
	}
}

// AC-2: the durable event id is identical across all interactive conns for a
// given logical event (it is the one ring id fanned to both), while the per-conn
// envelope ID counter differs between conns and resets meaning per reconnect.
func TestInteractiveTurnEmitterV2_WireEventIDIdenticalAcrossConns(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: true},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "Read"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	a := pushesFor(bcast.pushes, "a")
	b := pushesFor(bcast.pushes, "b")
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("per-conn push counts: a=%d b=%d, want equal and non-zero", len(a), len(b))
	}
	for i := range a {
		if a[i].env.EventID == nil || b[i].env.EventID == nil {
			t.Fatalf("event %d: nil EventID (a=%v b=%v)", i, a[i].env.EventID, b[i].env.EventID)
		}
		if *a[i].env.EventID != *b[i].env.EventID {
			t.Errorf("event %d: durable id differs across conns: a=%d b=%d", i, *a[i].env.EventID, *b[i].env.EventID)
		}
		if a[i].env.ID == b[i].env.ID {
			t.Errorf("event %d: per-conn env.ID should differ across conns, both = %d", i, a[i].env.ID)
		}
	}
}

// AC-3: the durable event ids a phone observes on one conversation's live stream
// are strictly increasing in emit order (the 1..N ring sequence), so the latest
// one a phone saw is a valid last_event_id.
func TestInteractiveTurnEmitterV2_WireEventIDsStrictlyIncreasing(t *testing.T) {
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

	ids := pushEventIDs(t, pushesFor(bcast.pushes, "a"))
	if len(ids) == 0 {
		t.Fatal("no pushes recorded")
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("event ids not strictly increasing: %v", ids)
		}
	}
	for i, id := range ids {
		if id != uint64(i+1) {
			t.Fatalf("event ids not the 1..N ring sequence: got %v", ids)
		}
	}
}
