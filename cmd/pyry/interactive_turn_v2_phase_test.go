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

// --- #2712 turnPhaseSnapshot: the emitter's cross-goroutine view of the open
// turn's last-sent phase, read by the relay's connect-time turn-phase reconcile.

const otherConvID = "22222222-2222-4222-8222-222222222222"

// phaseEmitter builds an emitter with one interactive conn and a snapshot.
func phaseEmitter(t *testing.T) (*interactiveTurnEmitterV2, *stubCursor, *fakeInteractiveBcast, *turnPhaseSnapshot) {
	t.Helper()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	snap := &turnPhaseSnapshot{}
	e.phases = snap
	return e, cur, bcast, snap
}

// phaseOf renders the snapshot as "conversation/state", or "" when no turn is
// running, failing on anything but zero or one payload.
func phaseOf(t *testing.T, p *turnPhaseSnapshot) string {
	t.Helper()
	got := p.running()
	switch len(got) {
	case 0:
		if got != nil {
			t.Fatalf("running() = empty non-nil slice, want nil")
		}
		return ""
	case 1:
		return got[0].ConversationID + "/" + got[0].State
	default:
		t.Fatalf("running() returned %d payloads, want at most 1", len(got))
		return ""
	}
}

// TestTurnPhaseSnapshot_TracksLifecycle walks one turn and checks the snapshot
// after each event: absent before any phase is sent, the sent phase while the
// turn runs, absent once the turn's idle is sent (AC1, AC2).
func TestTurnPhaseSnapshot_TracksLifecycle(t *testing.T) {
	t.Parallel()
	e, _, _, snap := phaseEmitter(t)
	ctx := context.Background()

	if got := phaseOf(t, snap); got != "" {
		t.Fatalf("before any event: phase = %q, want none", got)
	}
	steps := []struct {
		name string
		ev   turnevent.Event
		want string
	}{
		// ToolProgress opens a turn but sends no turn_state, so there is no
		// phase to re-assert: a phase never sent is never derived.
		{"turn opened without a transition", turnevent.ToolProgress{ToolCallID: "t0"}, ""},
		{"thinking", turnevent.ThoughtChunk{Text: "reasoning"}, testConvID + "/thinking"},
		{"responding", turnevent.TextChunk{Text: "hello"}, testConvID + "/responding"},
		{"tool keeps responding", turnevent.ToolStart{ToolCallID: "t1", Title: "Read"}, testConvID + "/responding"},
		{"back to thinking", turnevent.ThoughtChunk{Text: "more"}, testConvID + "/thinking"},
		{"turn end", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, ""},
	}
	for _, st := range steps {
		e.Handle(ctx, st.ev)
		if got := phaseOf(t, snap); got != st.want {
			t.Errorf("after %s: phase = %q, want %q", st.name, got, st.want)
		}
	}
}

// TestTurnPhaseSnapshot_ClearedWhenTurnClosedExternally covers the two paths
// that ended a turn without a TurnEnd event. closeForConversation sends the turn's
// idle and must clear only its own conversation's phase; #1062's follow-active
// switch, the other such path, is gone with #2739, so another conversation's
// phase must now survive.
func TestTurnPhaseSnapshot_ClearedWhenTurnClosedExternally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("closeForConversation", func(t *testing.T) {
		t.Parallel()
		e, _, bcast, snap := phaseEmitter(t)
		e.Handle(ctx, turnevent.ThoughtChunk{Text: "reasoning"})
		e.closeForConversation(ctx, otherConvID) // not the open turn: no-op
		if got := phaseOf(t, snap); got != testConvID+"/thinking" {
			t.Fatalf("after closing another conversation: phase = %q, want it kept", got)
		}
		e.closeForConversation(ctx, testConvID)
		if got := phaseOf(t, snap); got != "" {
			t.Fatalf("after closeForConversation: phase = %q, want none", got)
		}
		if got, want := turnStateValues(t, bcast.pushes), []string{"thinking", "idle"}; !slices.Equal(got, want) {
			t.Fatalf("turn_state = %v, want %v", got, want)
		}
	})

	t.Run("other conversation's turn kept", func(t *testing.T) {
		t.Parallel()
		e, _, _, snap := phaseEmitter(t)
		e.HandleFor(ctx, testConvID, turnevent.ThoughtChunk{Text: "a"})
		e.HandleFor(ctx, otherConvID, turnevent.ThoughtChunk{Text: "b"})
		if got := len(snap.running()); got != 2 {
			t.Fatalf("running() = %d payloads, want 2 (one per open turn, #2739)", got)
		}
		e.closeForConversation(ctx, testConvID)
		if got := phaseOf(t, snap); got != otherConvID+"/thinking" {
			t.Fatalf("after closing A: phase = %q, want B's kept", got)
		}
	})
}

// phaseProbeBcast records what the snapshot holds at each ActiveConns call,
// which is the moment the emitter asks the relay's Run goroutine for its
// fan-out. One interactive conn, so seen[i] pairs with pushes[i].
type phaseProbeBcast struct {
	fakeInteractiveBcast
	snap *turnPhaseSnapshot
	t    *testing.T
	seen []string
}

func (b *phaseProbeBcast) ActiveConns(ctx context.Context) []relay.ActiveConn {
	b.seen = append(b.seen, phaseOf(b.t, b.snap))
	return b.fakeInteractiveBcast.ActiveConns(ctx)
}

// TestTurnPhaseSnapshot_PublishedBeforeFanOut pins the ordering the relay's
// reconcile relies on (AC2): every turn_state transition is in the snapshot
// before the emitter asks for its fan-out. In particular a turn's idle has
// already cleared the snapshot by then, so a conn the reconcile reads as running
// is necessarily in the idle's fan-out; publishing after emit would let a
// connecting conn read thinking, miss the idle, and stay thinking.
func TestTurnPhaseSnapshot_PublishedBeforeFanOut(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	snap := &turnPhaseSnapshot{}
	bcast := &phaseProbeBcast{
		fakeInteractiveBcast: fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}},
		snap:                 snap,
		t:                    t,
	}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	e.phases = snap

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	if len(bcast.seen) != len(bcast.pushes) {
		t.Fatalf("ActiveConns calls = %d, pushes = %d; want one per envelope", len(bcast.seen), len(bcast.pushes))
	}
	var checked int
	for i, p := range bcast.pushes {
		if p.env.Type != protocol.TypeTurnState {
			continue
		}
		var ts protocol.TurnStatePayload
		if err := json.Unmarshal(p.env.Payload, &ts); err != nil {
			t.Fatalf("decode turn_state: %v", err)
		}
		want := testConvID + "/" + ts.State
		if ts.State == "idle" {
			want = ""
		}
		if bcast.seen[i] != want {
			t.Errorf("fan-out of turn_state %q: snapshot held %q, want %q", ts.State, bcast.seen[i], want)
		}
		checked++
	}
	if checked != 3 {
		t.Fatalf("checked %d turn_state envelopes, want 3 (thinking, responding, idle)", checked)
	}
}

// TestTurnPhaseSnapshot_ReadDoesNotDisturbEmission pins AC3's daemon half:
// reading the snapshot mid-turn, as every connecting conn does, leaves the
// emitter's de-duplication alone. A repeated same-phase event still emits
// nothing, and the next real transition emits exactly once — the same stream an
// emitter with no snapshot produces.
func TestTurnPhaseSnapshot_ReadDoesNotDisturbEmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	script := []turnevent.Event{
		turnevent.ThoughtChunk{Text: "t1"},
		turnevent.ThoughtChunk{Text: "t2"},
		turnevent.TextChunk{Text: "x1"},
		turnevent.TextChunk{Text: "x2"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	}

	run := func(withSnapshot bool) []string {
		cur := &stubCursor{}
		cur.set(testConvID)
		bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
		e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
		var snap *turnPhaseSnapshot
		if withSnapshot {
			snap = &turnPhaseSnapshot{}
			e.phases = snap
		}
		for _, ev := range script {
			e.Handle(ctx, ev)
			if snap != nil {
				_ = snap.running()
				_ = snap.running()
			}
		}
		return turnStateValues(t, bcast.pushes)
	}

	without, with := run(false), run(true)
	if want := []string{"thinking", "responding", "idle"}; !slices.Equal(without, want) {
		t.Fatalf("baseline turn_state = %v, want %v", without, want)
	}
	if !slices.Equal(with, without) {
		t.Fatalf("turn_state with snapshot reads = %v, want %v (same as without)", with, without)
	}
}
