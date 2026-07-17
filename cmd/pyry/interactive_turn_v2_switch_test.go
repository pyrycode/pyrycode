package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// switchConvA / switchConvB: two distinct valid UUIDv4 conversation ids for the
// shared-emitter follow-active switch tests (#1062). A single long-lived
// interactiveTurnEmitterV2 owns the turn lifecycle for every conversation on the
// interactive leg; these exercise a cursor move from A to B while A's turn is
// still open (its TurnEnd never delivered because the subscription was torn down
// mid-turn across the switch).
const (
	switchConvA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	switchConvB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// turnStateConvPairs returns (conversation_id, state) pairs for every turn_state
// push, in wire order. The sibling turnStateValues (interactive_turn_v2_test.go)
// drops the conversation scoping these tests assert.
func turnStateConvPairs(t *testing.T, pushes []recordedPush) [][2]string {
	t.Helper()
	var out [][2]string
	for _, p := range pushes {
		if p.env.Type != protocol.TypeTurnState {
			continue
		}
		var ts protocol.TurnStatePayload
		if err := json.Unmarshal(p.env.Payload, &ts); err != nil {
			t.Fatalf("decode turn_state: %v", err)
		}
		out = append(out, [2]string{ts.ConversationID, ts.State})
	}
	return out
}

// TestInteractiveTurnV2_SwitchWithOpenPriorTurn_EmitsResponding is the AC4 oracle
// (recovered from feature/1050's zz_repro). It models the follow-active switch
// onto conv B while conv A's turn is still open — interrupted / torn down mid-turn:
// no TurnEnd was delivered, so inTurn stays true and currentState stays
// StateResponding. The SAME shared emitter then handles conv B's first content.
// B must get a turn_state responding. On unfixed main the carried-over
// currentState de-dups B's opening transition away, so B streams a delta but NO
// turn_state (the reported symptom): RED. After the fix the guard abandons A's
// orphaned turn on the cursor move, so B opens fresh and emits its responding: GREEN.
func TestInteractiveTurnV2_SwitchWithOpenPriorTurn_EmitsResponding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cur := &stubCursor{}
	cur.set(switchConvA)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// Prior turn on conv A that never ends (interrupted before end_turn): opens a
	// turn (responding emitted), buffers + flushes one delta, NO TurnEnd.
	e.Handle(ctx, turnevent.TextChunk{MessageID: "a1", Text: "partial A"})
	e.flushDelta(ctx)

	// Follow-active switch: the active-conversation cursor moves to conv B. (In
	// production the subscriber tears down A's tail and re-subscribes onto B; the
	// emitter is the SAME instance and its lifecycle is NOT reset here.)
	cur.set(switchConvB)

	// Conv B's first turn: first content event.
	e.Handle(ctx, turnevent.TextChunk{MessageID: "b1", Text: "hello from B"})
	e.flushDelta(ctx)

	// Precondition (the reported symptom): B's delta DID reach the wire.
	sawBDelta := false
	for _, d := range assistantDeltas(t, bcast.pushes) {
		if d.ConversationID == switchConvB {
			sawBDelta = true
		}
	}
	if !sawBDelta {
		t.Fatalf("precondition: expected B's assistant_delta on the wire")
	}

	// The fix: a turn_state carries conv B.
	sawBTurnState := false
	for _, pair := range turnStateConvPairs(t, bcast.pushes) {
		if pair[0] == switchConvB {
			sawBTurnState = true
		}
	}
	if !sawBTurnState {
		t.Fatalf("conv B streamed a delta but NO turn_state; the open prior turn's "+
			"currentState de-duped its responding transition. turn_state pairs: %v",
			turnStateConvPairs(t, bcast.pushes))
	}
}

// TestInteractiveTurnV2_SwitchResetsTurnIdentity strengthens the recovered
// GreenWhenLifecycleResetOnSwitch: it does NOT manually reset the emitter (the fix
// does that itself on the cursor move) and asserts the full fresh-turn identity —
// B gets a responding turn_state, B's delta carries a turn id distinct from A's,
// and its seq is reset to 0. RED on main (no reset → de-dup + stale turn id / seq),
// GREEN after the fix.
func TestInteractiveTurnV2_SwitchResetsTurnIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cur := &stubCursor{}
	cur.set(switchConvA)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(ctx, turnevent.TextChunk{MessageID: "a1", Text: "partial A"})
	e.flushDelta(ctx)
	turnAID := e.turnID

	// The switch — no manual lifecycle reset; the emitter's own guard does it.
	cur.set(switchConvB)

	e.Handle(ctx, turnevent.TextChunk{MessageID: "b1", Text: "hello from B"})
	e.flushDelta(ctx)

	sawBResponding := false
	for _, pair := range turnStateConvPairs(t, bcast.pushes) {
		if pair[0] == switchConvB && pair[1] == "responding" {
			sawBResponding = true
		}
	}
	if !sawBResponding {
		t.Fatalf("conv B has no responding turn_state: %v", turnStateConvPairs(t, bcast.pushes))
	}

	deltas := assistantDeltas(t, bcast.pushes)
	bDelta := deltas[len(deltas)-1]
	if bDelta.ConversationID != switchConvB {
		t.Fatalf("last delta not conv B: %+v", bDelta)
	}
	if bDelta.TurnID == turnAID {
		t.Fatalf("conv B delta still carries A's stale turn id %q", turnAID)
	}
	if bDelta.Seq != 0 {
		t.Fatalf("conv B delta seq = %d, want 0 (fresh turn)", bDelta.Seq)
	}
}

// TestInteractiveTurnV2_SwitchFlushesAbandonedDeltaBeforeNewState asserts AC3
// scoping and wire ordering across the switch: when conv A has buffered but
// un-flushed text at the cursor move, the guard flushes it stamped with A's
// conversation_id BEFORE conv B's opening responding turn_state (stamped B). The
// abandoned text keeps correct attribution and precedes the new conversation's state.
func TestInteractiveTurnV2_SwitchFlushesAbandonedDeltaBeforeNewState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cur := &stubCursor{}
	cur.set(switchConvA)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// Conv A: open a turn and buffer a delta, but do NOT flush it (no message
	// boundary, no turn end) — the buffer still holds A's text at the switch.
	e.Handle(ctx, turnevent.TextChunk{MessageID: "a1", Text: "partial A"})

	// Follow-active switch with A's delta still buffered.
	cur.set(switchConvB)

	// Conv B's first content triggers the guard: flush A's buffered delta (stamped
	// A), abandon A's turn, then open B's fresh turn (responding stamped B).
	e.Handle(ctx, turnevent.TextChunk{MessageID: "b1", Text: "hello from B"})
	e.flushDelta(ctx)

	wantTypes := []string{
		protocol.TypeTurnState,      // A responding (A's turn opened)
		protocol.TypeAssistantDelta, // A's abandoned text, flushed by the guard
		protocol.TypeTurnState,      // B responding (fresh turn)
		protocol.TypeAssistantDelta, // B's text
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("switch envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].ConversationID != switchConvA || deltas[0].Text != "partial A" {
		t.Fatalf("abandoned delta: got conv=%q text=%q, want conv=%q text=%q",
			deltas[0].ConversationID, deltas[0].Text, switchConvA, "partial A")
	}
	if deltas[1].ConversationID != switchConvB {
		t.Fatalf("B delta conversation_id: got %q, want %q", deltas[1].ConversationID, switchConvB)
	}
	// The abandoned delta must be stamped A, and B's opening responding stamped B —
	// never crossed.
	wantPairs := [][2]string{{switchConvA, "responding"}, {switchConvB, "responding"}}
	if got := turnStateConvPairs(t, bcast.pushes); !slices.Equal(got, wantPairs) {
		t.Fatalf("turn_state (conv,state) pairs:\n got %v\nwant %v", got, wantPairs)
	}
}

// TestInteractiveTurnV2_CleanPriorTurnEndUnaffected is the control (recovered
// ControlCleanPriorTurnEndIsFine): when conv A's turn ends CLEANLY (a TurnEnd
// arrives → inTurn=false, currentState=idle) before the cursor moves, the switch
// to conv B works with no extra reset — B still gets its responding. Isolates the
// defect to the open-prior-turn carryover and guards against the guard misfiring
// on the normal path. Passes both on main and after the fix.
func TestInteractiveTurnV2_CleanPriorTurnEndUnaffected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cur := &stubCursor{}
	cur.set(switchConvA)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(ctx, turnevent.TextChunk{MessageID: "a1", Text: "partial A"})
	e.Handle(ctx, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}) // clean close
	cur.set(switchConvB)
	e.Handle(ctx, turnevent.TextChunk{MessageID: "b1", Text: "hello from B"})
	e.flushDelta(ctx)

	sawBResponding := false
	for _, pair := range turnStateConvPairs(t, bcast.pushes) {
		if pair[0] == switchConvB && pair[1] == "responding" {
			sawBResponding = true
		}
	}
	if !sawBResponding {
		t.Fatalf("control: even after clean TurnEnd, B has no responding: %v", turnStateConvPairs(t, bcast.pushes))
	}
}
