package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// switchConvA / switchConvB: two distinct valid UUIDv4 conversation ids for the
// per-conversation turn-state tests (#2739). They replace #1062's follow-active
// switch tests: a cursor move no longer ends the prior conversation's turn,
// because that conversation's events keep arriving under its own id.
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

func newTwoConvEmitter(t *testing.T) (*interactiveTurnEmitterV2, *fakeInteractiveBcast, *turnPhaseSnapshot) {
	t.Helper()
	cur := &stubCursor{}
	cur.set(switchConvB) // the cursor names B throughout; HandleFor must not read it
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	snap := &turnPhaseSnapshot{}
	e.phases = snap
	return e, bcast, snap
}

// TestInteractiveTurnV2_InterleavedConversationsKeepOwnTurns interleaves two
// conversations' turns on one emitter. Each keeps its own turn id, seq numbering
// and buffered text; A's turn end leaves B's turn open; and the reconcile
// snapshot reports both running turns, not only the last one touched.
func TestInteractiveTurnV2_InterleavedConversationsKeepOwnTurns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, bcast, snap := newTwoConvEmitter(t)

	e.HandleFor(ctx, switchConvA, turnevent.TextChunk{MessageID: "ma", Text: "a1 "})
	e.HandleFor(ctx, switchConvB, turnevent.TextChunk{MessageID: "mb", Text: "b1"})
	// Same message on A: appends to A's own buffer, untouched by B's text between.
	e.HandleFor(ctx, switchConvA, turnevent.TextChunk{MessageID: "ma", Text: "a2"})

	running := snap.running()
	if len(running) != 2 || running[0].ConversationID != switchConvA || running[1].ConversationID != switchConvB {
		t.Fatalf("running() with two open turns = %+v, want one entry each for A and B", running)
	}

	e.HandleFor(ctx, switchConvA, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	if st, ok := e.turns[switchConvB]; !ok || !st.inTurn {
		t.Fatalf("B's turn closed by A's turn end; want it still open")
	}
	if got := phaseOf(t, snap); got != switchConvB+"/responding" {
		t.Fatalf("after A's turn end: phase = %q, want B's alone", got)
	}

	// B's next content opens no new turn: its delta keeps B's turn id and seq 0.
	e.HandleFor(ctx, switchConvB, turnevent.ToolStart{ToolCallID: "tb", Title: "Read"})
	e.HandleFor(ctx, switchConvB, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("assistant deltas = %d, want 2", len(deltas))
	}
	a, b := deltas[0], deltas[1]
	if a.ConversationID != switchConvA || a.Text != "a1 a2" || a.Seq != 0 {
		t.Errorf("A's delta = {conv:%q text:%q seq:%d}, want {%q %q 0}", a.ConversationID, a.Text, a.Seq, switchConvA, "a1 a2")
	}
	if b.ConversationID != switchConvB || b.Text != "b1" || b.Seq != 0 {
		t.Errorf("B's delta = {conv:%q text:%q seq:%d}, want {%q %q 0}", b.ConversationID, b.Text, b.Seq, switchConvB, "b1")
	}
	if a.TurnID == b.TurnID || a.TurnID == "" || b.TurnID == "" {
		t.Errorf("turn ids A=%q B=%q, want two distinct non-empty ids", a.TurnID, b.TurnID)
	}

	wantStates := [][2]string{
		{switchConvA, "responding"},
		{switchConvB, "responding"},
		{switchConvA, "idle"},
		{switchConvB, "idle"},
	}
	if got := turnStateConvPairs(t, bcast.pushes); !slices.Equal(got, wantStates) {
		t.Fatalf("turn_state pairs:\n got %v\nwant %v", got, wantStates)
	}

	var turnEnds []string
	for _, p := range bcast.pushes {
		if p.env.Type != protocol.TypeTurnEnd {
			continue
		}
		var te protocol.TurnEndPayload
		if err := json.Unmarshal(p.env.Payload, &te); err != nil {
			t.Fatalf("decode turn_end: %v", err)
		}
		turnEnds = append(turnEnds, te.ConversationID)
	}
	if !slices.Equal(turnEnds, []string{switchConvA, switchConvB}) {
		t.Errorf("turn_end conversations = %v, want [A B]", turnEnds)
	}
	if got := phaseOf(t, snap); got != "" {
		t.Errorf("after both turn ends: phase = %q, want none", got)
	}
	if len(e.turns) != 0 {
		t.Errorf("turn state retained for %d conversations after both turns ended, want 0", len(e.turns))
	}
}

// TestInteractiveTurnV2_SharedTimerFlushesOtherConversation: the coalescing timer
// is shared, so a flush of A's buffer must not stop it while B still holds text,
// and the timer's fire (flushAll) must emit B's delta under B's id.
func TestInteractiveTurnV2_SharedTimerFlushesOtherConversation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, bcast, _ := newTwoConvEmitter(t)

	e.HandleFor(ctx, switchConvA, turnevent.TextChunk{MessageID: "ma", Text: "from A"})
	e.HandleFor(ctx, switchConvB, turnevent.TextChunk{MessageID: "mb", Text: "from B"})
	// A's tool start flushes A's buffer only.
	e.HandleFor(ctx, switchConvA, turnevent.ToolStart{ToolCallID: "ta", Title: "Read"})

	select {
	case <-e.flushC():
	case <-time.After(5 * time.Second):
		t.Fatal("flush timer never fired while B still held buffered text")
	}
	e.flushAll(ctx)

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("assistant deltas = %d, want 2", len(deltas))
	}
	if deltas[1].ConversationID != switchConvB || deltas[1].Text != "from B" {
		t.Errorf("timer-flushed delta = {conv:%q text:%q}, want {%q %q}",
			deltas[1].ConversationID, deltas[1].Text, switchConvB, "from B")
	}
	if e.anyBuffered() {
		t.Error("text still buffered after flushAll")
	}
}

// TestInteractiveTurnV2_FlushAllFlushesEveryConversation: one timer fire flushes
// every conversation's buffer, each under its own conversation and turn id.
func TestInteractiveTurnV2_FlushAllFlushesEveryConversation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, bcast, _ := newTwoConvEmitter(t)

	e.HandleFor(ctx, switchConvA, turnevent.TextChunk{MessageID: "ma", Text: "A text"})
	e.HandleFor(ctx, switchConvB, turnevent.TextChunk{MessageID: "mb", Text: "B text"})
	e.flushAll(ctx)

	got := map[string]string{}
	for _, d := range assistantDeltas(t, bcast.pushes) {
		got[d.ConversationID] = d.Text
	}
	if got[switchConvA] != "A text" || got[switchConvB] != "B text" || len(got) != 2 {
		t.Fatalf("flushed deltas by conversation = %v, want A text / B text", got)
	}
}

// TestInteractiveTurnV2_CleanPriorTurnEndUnaffected: a conversation whose turn
// ended cleanly opens its next turn with a fresh turn id and a responding state.
func TestInteractiveTurnV2_CleanPriorTurnEndUnaffected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, bcast, _ := newTwoConvEmitter(t)

	e.HandleFor(ctx, switchConvA, turnevent.TextChunk{MessageID: "a1", Text: "one"})
	e.HandleFor(ctx, switchConvA, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	e.HandleFor(ctx, switchConvA, turnevent.TextChunk{MessageID: "a2", Text: "two"})
	e.flushAll(ctx)

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 || deltas[0].TurnID == deltas[1].TurnID {
		t.Fatalf("deltas = %+v, want two with distinct turn ids", deltas)
	}
	want := [][2]string{{switchConvA, "responding"}, {switchConvA, "idle"}, {switchConvA, "responding"}}
	if got := turnStateConvPairs(t, bcast.pushes); !slices.Equal(got, want) {
		t.Fatalf("turn_state pairs:\n got %v\nwant %v", got, want)
	}
}
