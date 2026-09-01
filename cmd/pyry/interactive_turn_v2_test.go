package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// testConvID is a fixed valid UUIDv4 conversation id shared across the cmd/pyry
// turn-stream tests. Relocated here from the deleted assistant_turn_test.go
// (#913 v1 retirement); its sibling coarse-bridge const testChunk deleted with
// that file.
const testConvID = "11111111-1111-4111-8111-111111111111"

// stubCursor is a race-safe cursorReader test double: set() stores the current
// conversation id, CurrentConversation() reads it. Shared by the v2 turn-emitter
// and turn-stream tests. Relocated here from the deleted assistant_turn_test.go.
type stubCursor struct{ id atomic.Value }

func (s *stubCursor) CurrentConversation() string {
	v := s.id.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

func (s *stubCursor) set(id string) { s.id.Store(id) }

// discardLogger returns a slog logger that writes nowhere — the default logger
// for cmd/pyry tests that need one but assert on nothing it emits. Relocated
// here from the deleted assistant_turn_test.go.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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
//
// #1638 AC#3 extends this with claude's announced model, which is the same #833
// posture one value over ("model / effort / YOLO values are NEVER logged at any
// level", restated in internal/relay's v2session_settings.go and
// internal/sessions' pool.go). It is driven THROUGH the new Handle arm, which is
// what this rig — a live cursor plus a conn whose Push always fails — puts under
// test. The two model assertions at the bottom are a PAIR and the pairing is the
// point: log-absence alone is passed by a Handle arm that silently drops the
// event, so the payload-presence half is what discriminates. #1600 deferred this
// on the grounds that no Handle arm was being added; that deferral expires here.
func TestInteractiveTurnEmitterV2_NoAppOutputLogLeak(t *testing.T) {
	t.Parallel()
	const (
		secretThought   = "SECRETTHOUGHTZZZ"
		secretAssistant = "SECRETASSISTANTZZZ"
		secretToolTitle = "SECRETTOOLTITLEZZZ"
		secretToolInput = "SECRETINPUTZZZ"
		secretToolReslt = "SECRETRESULTZZZ"
		secretModel     = "SecretModelZZZ"
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
		// The two turn-orthogonal frames go first on purpose: nothing is buffered
		// yet, so each arm's flushDelta is a no-op and the five events below behave
		// exactly as they did before #1638. They also exercise the no-turn-open path
		// for free. Safe against the fake — ActiveConns clamps to the last snapshot
		// in steady state rather than running out of them.
		turnevent.ModelAnnounced{Model: secretModel, Truncated: true},
		// #1849 AC#5 one variant over: the list's strings are the same #833 values
		// multiplied per entry. Driven through the same log-heavy rig, and paired
		// with the payload-presence check at the bottom.
		emitterModelListFixture,
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
	leakable := append([]string{secretThought, secretAssistant, secretToolTitle, secretToolInput, secretToolReslt, secretModel},
		emitterModelListSentinels()...)
	for _, secret := range leakable {
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

	// MIND THE POLARITY: the loop directly above requires its sentinel to be
	// ABSENT from every payload; this one requires the model to be PRESENT. The
	// log half above is passed by a Handle arm that silently drops the event, so
	// without this the pair does not discriminate. fakeInteractiveBcast.Push
	// records the attempt BEFORE returning its error, so the envelope is here even
	// though this conn's push failed.
	//
	// Deliberately NOT asserted: that kind=model_announced appears in the log. It
	// is unsatisfiable on this path — push_err carries no kind field, and the only
	// records that log eventKind are the no-cursor drop (which returns before the
	// type switch), Handle's default and emitMapped's unmapped branch, the last two
	// of which the new arm makes unreachable for this variant. The positive control
	// proving the event traversed the log-heavy path is the `logs == ""` Fatal
	// above, which the push_err branch keeps firing.
	var announced *protocol.ModelAnnouncedPayload
	for _, p := range bcast.pushes {
		if p.env.Type != protocol.TypeModelAnnounced {
			continue
		}
		var pl protocol.ModelAnnouncedPayload
		if err := json.Unmarshal(p.env.Payload, &pl); err != nil {
			t.Fatalf("decode model_announced payload: %v", err)
		}
		announced = &pl
		break
	}
	if announced == nil {
		t.Fatalf("no %s envelope reached the wire; the Handle arm dropped the event", protocol.TypeModelAnnounced)
	}
	if announced.Model != secretModel {
		t.Fatalf("announced model on the wire: got %q, want %q", announced.Model, secretModel)
	}

	// The same polarity pair for the list (#1849): its sentinels are required
	// ABSENT from the log above and PRESENT on the wire here, and again the second
	// half is what discriminates — a Handle arm that dropped the event passes the
	// log half on its own.
	var listed *protocol.ModelListPayload
	for _, p := range bcast.pushes {
		if p.env.Type != protocol.TypeModelList {
			continue
		}
		var pl protocol.ModelListPayload
		if err := json.Unmarshal(p.env.Payload, &pl); err != nil {
			t.Fatalf("decode model_list payload: %v", err)
		}
		listed = &pl
		break
	}
	if listed == nil {
		t.Fatalf("no %s envelope reached the wire; the Handle arm dropped the event", protocol.TypeModelList)
	}
	if len(listed.Models) != len(emitterModelListFixture.Models) {
		t.Fatalf("model_list rows on the wire: got %d, want %d", len(listed.Models), len(emitterModelListFixture.Models))
	}
	if listed.Models[0].ResolvedModel != emitterModelListFixture.Models[0].ResolvedModel {
		t.Fatalf("first row's resolved model on the wire: got %q, want %q",
			listed.Models[0].ResolvedModel, emitterModelListFixture.Models[0].ResolvedModel)
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

// AC#1/#4: an ApiRetry fans out as an api_retry envelope to interactive-capable
// conns only (the capability gate), carrying the cursor's conversation_id, the
// active edge, and the parsed attempt counter.
func TestInteractiveTurnEmitterV2_ApiRetryFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ApiRetry{Active: true, Current: 3, Total: 10})

	if got := len(bcast.pushes); got != 1 {
		t.Fatalf("api_retry pushed %d envelopes; want exactly 1 (interactive conn only)", got)
	}
	p := bcast.pushes[0]
	if p.connID != "a" {
		t.Fatalf("api_retry pushed to conn %q; want interactive conn %q", p.connID, "a")
	}
	if p.env.Type != protocol.TypeApiRetry {
		t.Fatalf("envelope type: got %q, want %q", p.env.Type, protocol.TypeApiRetry)
	}
	var ap protocol.ApiRetryPayload
	if err := json.Unmarshal(p.env.Payload, &ap); err != nil {
		t.Fatalf("decode api_retry payload: %v", err)
	}
	if ap.ConversationID != testConvID {
		t.Fatalf("api_retry conversation_id: got %q, want %q", ap.ConversationID, testConvID)
	}
	if !ap.Active || ap.Current != 3 || ap.Total != 10 {
		t.Fatalf("api_retry payload: got %+v, want {active:true current:3 total:10}", ap)
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatal("non-interactive conn b received the api_retry")
	}
}

// AC#2/#4: a Compacting fans out as a compacting envelope to interactive-capable
// conns only, carrying conversation_id + the active edge (banner-only, no counter).
func TestInteractiveTurnEmitterV2_CompactingFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.Compacting{Active: true})

	if got := len(bcast.pushes); got != 1 {
		t.Fatalf("compacting pushed %d envelopes; want exactly 1 (interactive conn only)", got)
	}
	p := bcast.pushes[0]
	if p.connID != "a" {
		t.Fatalf("compacting pushed to conn %q; want interactive conn %q", p.connID, "a")
	}
	if p.env.Type != protocol.TypeCompacting {
		t.Fatalf("envelope type: got %q, want %q", p.env.Type, protocol.TypeCompacting)
	}
	var cp protocol.CompactingPayload
	if err := json.Unmarshal(p.env.Payload, &cp); err != nil {
		t.Fatalf("decode compacting payload: %v", err)
	}
	if cp.ConversationID != testConvID {
		t.Fatalf("compacting conversation_id: got %q, want %q", cp.ConversationID, testConvID)
	}
	if !cp.Active {
		t.Fatalf("compacting active: got %v, want true", cp.Active)
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatal("non-interactive conn b received the compacting")
	}
}

// AC#4: the status peers mutate no turn lifecycle — a bare ApiRetry then
// Compacting before any turn emit only their own frames (no turn_state / delta),
// and the next content still opens a fresh turn as if they never happened.
func TestInteractiveTurnEmitterV2_StatusPeersNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// Bare status peers before any turn: only their own frames, no turn_state / delta.
	e.Handle(context.Background(), turnevent.ApiRetry{Active: true, Current: 3, Total: 10})
	e.Handle(context.Background(), turnevent.Compacting{Active: true})
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeApiRetry, protocol.TypeCompacting}) {
		t.Fatalf("bare status-peer envelopes: got %v, want [%s %s]", got, protocol.TypeApiRetry, protocol.TypeCompacting)
	}

	// The next content opens a fresh turn: first subsequent envelope is
	// turn_state: responding (the status peers left inTurn/currentState untouched).
	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background()) // emit the coalesced delta (models a flush)
	wantTypes := []string{
		protocol.TypeApiRetry,
		protocol.TypeCompacting,
		protocol.TypeTurnState,      // responding — fresh turn opens after the status peers
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-status-peer envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after status peers: got %v, want [responding]", got)
	}
}

// AC#1/#3: the api-retry count re-fire and the clear edge both reach the wire.
// A shown-3 → shown-4 → hidden sequence produces three api_retry frames carrying
// current 3, 4, 4 and active true, true, false.
func TestInteractiveTurnEmitterV2_ApiRetryClearAndRefire(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ApiRetry{Active: true, Current: 3, Total: 10},
		turnevent.ApiRetry{Active: true, Current: 4, Total: 10},
		turnevent.ApiRetry{Active: false, Current: 4, Total: 10},
	} {
		e.Handle(context.Background(), ev)
	}

	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeApiRetry, protocol.TypeApiRetry, protocol.TypeApiRetry}) {
		t.Fatalf("api_retry frame types: got %v, want three api_retry", got)
	}
	var gotCurrent []int
	var gotActive []bool
	for _, p := range bcast.pushes {
		var ap protocol.ApiRetryPayload
		if err := json.Unmarshal(p.env.Payload, &ap); err != nil {
			t.Fatalf("decode api_retry payload: %v", err)
		}
		gotCurrent = append(gotCurrent, ap.Current)
		gotActive = append(gotActive, ap.Active)
	}
	if !slices.Equal(gotCurrent, []int{3, 4, 4}) {
		t.Fatalf("api_retry current sequence: got %v, want [3 4 4]", gotCurrent)
	}
	if !slices.Equal(gotActive, []bool{true, true, false}) {
		t.Fatalf("api_retry active sequence: got %v, want [true true false]", gotActive)
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

// An Unrecognized fans out as an unrecognized_message envelope to
// interactive-capable conns only (the capability gate), carrying the cursor's
// conversation_id plus the drop site, the offending type, the raw JSON, and the
// truncated flag.
func TestInteractiveTurnEmitterV2_UnrecognizedFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.Unrecognized{
		Site:      turnevent.UnrecognizedLineType,
		Kind:      "some_future_event",
		Raw:       `{"type":"some_future_event"}`,
		Truncated: true,
	})

	if got := len(bcast.pushes); got != 1 {
		t.Fatalf("unrecognized pushed %d envelopes; want exactly 1 (interactive conn only)", got)
	}
	p := bcast.pushes[0]
	if p.connID != "a" {
		t.Fatalf("unrecognized pushed to conn %q; want interactive conn %q", p.connID, "a")
	}
	if p.env.Type != protocol.TypeUnrecognizedMessage {
		t.Fatalf("envelope type: got %q, want %q", p.env.Type, protocol.TypeUnrecognizedMessage)
	}
	var up protocol.UnrecognizedMessagePayload
	if err := json.Unmarshal(p.env.Payload, &up); err != nil {
		t.Fatalf("decode unrecognized_message payload: %v", err)
	}
	if up.ConversationID != testConvID {
		t.Fatalf("conversation_id: got %q, want %q", up.ConversationID, testConvID)
	}
	if up.Site != "line_type" || up.MessageType != "some_future_event" {
		t.Fatalf("site/message_type: got %q/%q, want line_type/some_future_event", up.Site, up.MessageType)
	}
	if up.Raw != `{"type":"some_future_event"}` {
		t.Fatalf("raw: got %q, want the offending JSON", up.Raw)
	}
	if !up.Truncated {
		t.Fatal("truncated flag did not survive the mapping")
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatal("non-interactive conn b received the unrecognized_message")
	}
}

// An Unrecognized mutates no turn lifecycle. This is the load-bearing assertion
// of the whole variant: we do not know what the message is, so it must neither
// open nor close a turn. Opening one would wedge the conversation, because no
// turn end follows a message we could not understand.
func TestInteractiveTurnEmitterV2_UnrecognizedNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// Bare unrecognized before any turn: its own frame only, no turn_state.
	e.Handle(context.Background(), turnevent.Unrecognized{
		Site: turnevent.UnrecognizedLineType,
		Kind: "some_future_event",
		Raw:  `{"type":"some_future_event"}`,
	})
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeUnrecognizedMessage}) {
		t.Fatalf("bare unrecognized envelopes: got %v, want [%s]", got, protocol.TypeUnrecognizedMessage)
	}

	// The next content still opens a fresh turn, as if it never happened.
	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeUnrecognizedMessage,
		protocol.TypeTurnState,      // responding — fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-unrecognized envelopes:\n got %v\nwant %v", got, wantTypes)
	}
}

// Buffered assistant text keeps its wire position AHEAD of an unrecognized
// frame, matching the flush-first discipline every status peer follows.
func TestInteractiveTurnEmitterV2_UnrecognizedFlushesPendingDeltaFirst(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "before"})
	e.Handle(context.Background(), turnevent.Unrecognized{
		Site: turnevent.UnrecognizedAssistantBlock,
		Kind: "fake_future_block",
		Raw:  `{"type":"fake_future_block"}`,
	})

	wantTypes := []string{
		protocol.TypeTurnState,           // responding, opening the turn
		protocol.TypeAssistantDelta,      // "before", flushed ahead of the diagnostic
		protocol.TypeUnrecognizedMessage, //
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("envelope order:\n got %v\nwant %v", got, wantTypes)
	}
}

// --- background-task frames (#1394) ---------------------------------------

// bgTaskEvents is the three background-task variants with every field
// populated, shared by the fan-out and flush-ordering tests below so the three
// stay asserted as one family.
func bgTaskEvents() []struct {
	name    string
	ev      turnevent.Event
	wantTyp string
} {
	return []struct {
		name    string
		ev      turnevent.Event
		wantTyp string
	}{
		{
			name: "started",
			ev: turnevent.BackgroundTaskStarted{
				TaskID:          "task-1",
				ToolCallID:      "tool-9",
				Description:     "sleep 300 && echo done",
				TaskType:        "local_bash",
				TruncatedFields: []string{"description"},
			},
			wantTyp: protocol.TypeBackgroundTaskStarted,
		},
		{
			name: "updated",
			ev: turnevent.BackgroundTaskUpdated{
				TaskID:          "task-1",
				Patch:           `{"is_backgrounded":true}`,
				TruncatedFields: []string{"patch"},
			},
			wantTyp: protocol.TypeBackgroundTaskUpdated,
		},
		{
			name: "roster",
			ev: turnevent.BackgroundTaskRoster{
				Tasks: []turnevent.BackgroundTask{
					{TaskID: "task-1", TaskType: "local_bash", Description: "sleep 300"},
					{TaskID: "task-2", TaskType: "local_bash", Description: "tail -f log", TruncatedFields: []string{"description"}},
				},
				DroppedTasks: 3,
			},
			wantTyp: protocol.TypeBackgroundTaskRoster,
		},
	}
}

// AC#1: each background-task event reaches an interactive mobile client as its
// v2 frame (the capability gate holds), carrying every field of the event —
// including both truncation reports, the per-field truncated_fields and the
// roster's dropped_tasks — plus the conversation identity the bridge injects.
func TestInteractiveTurnEmitterV2_BackgroundTasksFanOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	for _, tt := range bgTaskEvents() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cur := &stubCursor{}
			cur.set(testConvID)
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
				{ConnID: "a", Interactive: true},
				{ConnID: "b", Interactive: false},
			}}}
			e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

			e.Handle(context.Background(), tt.ev)

			if got := len(bcast.pushes); got != 1 {
				t.Fatalf("pushed %d envelopes; want exactly 1 (interactive conn only)", got)
			}
			p := bcast.pushes[0]
			if p.connID != "a" {
				t.Fatalf("pushed to conn %q; want interactive conn %q", p.connID, "a")
			}
			if p.env.Type != tt.wantTyp {
				t.Fatalf("envelope type: got %q, want %q", p.env.Type, tt.wantTyp)
			}
			if len(pushesFor(bcast.pushes, "b")) != 0 {
				t.Fatalf("non-interactive conn b received the %s frame", tt.wantTyp)
			}

			switch ev := tt.ev.(type) {
			case turnevent.BackgroundTaskStarted:
				var got protocol.BackgroundTaskStartedPayload
				if err := json.Unmarshal(p.env.Payload, &got); err != nil {
					t.Fatalf("decode background_task_started payload: %v", err)
				}
				want := protocol.BackgroundTaskStartedPayload{
					ConversationID:  testConvID,
					TaskID:          ev.TaskID,
					ToolCallID:      ev.ToolCallID,
					Description:     ev.Description,
					TaskType:        ev.TaskType,
					TruncatedFields: ev.TruncatedFields,
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("payload:\n got %#v\nwant %#v", got, want)
				}
			case turnevent.BackgroundTaskUpdated:
				var got protocol.BackgroundTaskUpdatedPayload
				if err := json.Unmarshal(p.env.Payload, &got); err != nil {
					t.Fatalf("decode background_task_updated payload: %v", err)
				}
				want := protocol.BackgroundTaskUpdatedPayload{
					ConversationID:  testConvID,
					TaskID:          ev.TaskID,
					Patch:           ev.Patch,
					TruncatedFields: ev.TruncatedFields,
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("payload:\n got %#v\nwant %#v", got, want)
				}
			case turnevent.BackgroundTaskRoster:
				var got protocol.BackgroundTaskRosterPayload
				if err := json.Unmarshal(p.env.Payload, &got); err != nil {
					t.Fatalf("decode background_task_roster payload: %v", err)
				}
				want := protocol.BackgroundTaskRosterPayload{
					ConversationID: testConvID,
					Tasks: []protocol.BackgroundTask{
						{TaskID: "task-1", TaskType: "local_bash", Description: "sleep 300"},
						{TaskID: "task-2", TaskType: "local_bash", Description: "tail -f log", TruncatedFields: []string{"description"}},
					},
					// The truncation report that is not called truncated_fields:
					// losing it tells a phone a capped roster is the whole roster.
					DroppedTasks: 3,
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("payload:\n got %#v\nwant %#v", got, want)
				}
			}
		})
	}
}

// AC#2: a background-task frame opens and closes no turn. All three are handled
// bare before any turn and emit only their own frames — no turn_state, no
// turn_end — and the next content still opens a fresh turn, proving
// inTurn/turnID/currentState were left untouched.
func TestInteractiveTurnEmitterV2_BackgroundTasksNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, tt := range bgTaskEvents() {
		e.Handle(context.Background(), tt.ev)
	}
	bare := []string{
		protocol.TypeBackgroundTaskStarted,
		protocol.TypeBackgroundTaskUpdated,
		protocol.TypeBackgroundTaskRoster,
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, bare) {
		t.Fatalf("bare background-task envelopes: got %v, want %v", got, bare)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := append(append([]string{}, bare...),
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	)
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-background-task envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after background tasks: got %v, want [responding]", got)
	}
}

// AC#2: buffered assistant text keeps its wire position AHEAD of a
// background-task frame, matching the flush-first discipline every status peer
// follows.
func TestInteractiveTurnEmitterV2_BackgroundTasksFlushPendingDeltaFirst(t *testing.T) {
	t.Parallel()
	for _, tt := range bgTaskEvents() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cur := &stubCursor{}
			cur.set(testConvID)
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

			e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "before"})
			e.Handle(context.Background(), tt.ev)

			wantTypes := []string{
				protocol.TypeTurnState,      // responding, opening the turn
				protocol.TypeAssistantDelta, // "before", flushed ahead of the frame
				tt.wantTyp,
			}
			if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
				t.Fatalf("envelope order:\n got %v\nwant %v", got, wantTypes)
			}
		})
	}
}

// AC#2: a roster interleaved mid-turn does not disturb the open turn — the turn
// id is unchanged across the interleave, seq keeps advancing, and exactly one
// turn_end is emitted at the end.
func TestInteractiveTurnEmitterV2_BackgroundTaskRosterMidTurnDoesNotDisturbOpenTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{Text: "a1"},                 // opens turn: responding, buffers a1
		turnevent.BackgroundTaskRoster{DroppedTasks: 0}, // flushes a1 (seq 0) first
		turnevent.TextChunk{Text: "a2"},                 // buffers a2, seq 1 on flush
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	wantTypes := []string{
		protocol.TypeTurnState,            // responding
		protocol.TypeAssistantDelta,       // a1 (flushed by the roster)
		protocol.TypeBackgroundTaskRoster, // no surrounding turn_state
		protocol.TypeAssistantDelta,       // a2 (flushed by turn_end)
		protocol.TypeTurnEnd,              //
		protocol.TypeTurnState,            // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn roster envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("roster disrupted seq: got %d,%d want 0,1", deltas[0].Seq, deltas[1].Seq)
	}
	if deltas[0].TurnID != deltas[1].TurnID {
		t.Fatalf("roster split the turn: %q vs %q", deltas[0].TurnID, deltas[1].TurnID)
	}
}

// AC#3: a turn that backgrounds nothing gains no traffic. Paired with the
// non-suppression assertions above (MapEvent forwards an empty roster, and
// TestInteractiveTurnEmitterV2_BackgroundTaskRosterEmptyTasksOnTheWire proves it
// reaches the wire), this says "absent because unproduced", not "absent because
// filtered".
func TestInteractiveTurnEmitterV2_OrdinaryTurnEmitsNoBackgroundTaskFrames(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{Text: "hello"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "Bash", RawInput: json.RawMessage(`{"command":"ls"}`)},
		turnevent.ToolUpdate{ToolCallID: "t1", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "ok"}},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	if len(bcast.pushes) == 0 {
		t.Fatal("ordinary turn produced no envelopes at all; the test would not prove the property")
	}
	bgTypes := []string{
		protocol.TypeBackgroundTaskStarted,
		protocol.TypeBackgroundTaskUpdated,
		protocol.TypeBackgroundTaskRoster,
	}
	for _, got := range pushTypes(bcast.pushes) {
		if slices.Contains(bgTypes, got) {
			t.Fatalf("ordinary turn emitted a %q frame; want none", got)
		}
	}
}

// AC#3: an empty roster reaches the wire as "tasks": [] — asserted on the bytes
// the mapping produced from an empty turnevent.BackgroundTaskRoster, never on a
// payload the test built. This is the end-to-end form of the turnbridge
// assertion and is the property most easily broken by a well-meaning make(...)
// in the roster loop.
func TestInteractiveTurnEmitterV2_BackgroundTaskRosterEmptyTasksOnTheWire(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.BackgroundTaskRoster{})

	if got := len(bcast.pushes); got != 1 {
		t.Fatalf("empty roster pushed %d envelopes; want exactly 1 (it is a signal, not an absence)", got)
	}
	p := bcast.pushes[0]
	if p.env.Type != protocol.TypeBackgroundTaskRoster {
		t.Fatalf("envelope type: got %q, want %q", p.env.Type, protocol.TypeBackgroundTaskRoster)
	}
	if !bytes.Contains(p.env.Payload, []byte(`"tasks":[]`)) {
		t.Fatalf(`empty roster did not reach the wire as "tasks":[]:\n%s`, p.env.Payload)
	}
	if bytes.Contains(p.env.Payload, []byte(`"tasks":null`)) {
		t.Fatalf("empty roster reached the wire as null:\n%s", p.env.Payload)
	}
}

// AC#4: no claude-derived text from the three background-task events reaches a
// log, on the log-heaviest path (every push fails, firing the push_err DEBUG
// branch for every envelope).
func TestInteractiveTurnEmitterV2_BackgroundTasksNoLogLeak(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer // synchronous single-goroutine capture: bytes.Buffer is safe here
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}},
		pushErr:   map[string]error{"a": relay.ErrConnNotFound},
	}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	// A distinct marker in EVERY claude-derived string of all three variants,
	// including each roster row's three.
	secrets := []string{
		"SECRETTASKIDZZZ", "SECRETTOOLCALLZZZ", "SECRETDESCZZZ", "SECRETTASKTYPEZZZ",
		"SECRETPATCHZZZ",
		"SECRETROWIDZZZ", "SECRETROWTYPEZZZ", "SECRETROWDESCZZZ",
	}
	for _, ev := range []turnevent.Event{
		turnevent.BackgroundTaskStarted{
			TaskID:      "SECRETTASKIDZZZ",
			ToolCallID:  "SECRETTOOLCALLZZZ",
			Description: "SECRETDESCZZZ",
			TaskType:    "SECRETTASKTYPEZZZ",
		},
		turnevent.BackgroundTaskUpdated{TaskID: "SECRETTASKIDZZZ", Patch: "SECRETPATCHZZZ"},
		turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{
			{TaskID: "SECRETROWIDZZZ", TaskType: "SECRETROWTYPEZZZ", Description: "SECRETROWDESCZZZ"},
		}},
	} {
		e.Handle(context.Background(), ev)
	}

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected DEBUG push-error logs; got none (test would not prove the no-leak property)")
	}
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Fatalf("claude-derived text %q leaked into logs:\n%s", secret, logs)
		}
	}
}

// AC#4: eventKind's background-task arms are live code and content-free. The
// empty-cursor drop is the reachable eventKind call site for these variants on
// this lane, so the debug log must name the variant — not kind=unknown, and not
// any claude-derived field. The positive kind= assertion is what pins the arms;
// a leak-only assertion would pass against a missing arm.
func TestInteractiveTurnEmitterV2_BackgroundTasksEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ev       turnevent.Event
		wantKind string
	}{
		{
			name: "started",
			ev: turnevent.BackgroundTaskStarted{
				TaskID: "SECRETKINDZZZ", ToolCallID: "SECRETKINDZZZ",
				Description: "SECRETKINDZZZ", TaskType: "SECRETKINDZZZ",
			},
			wantKind: "background_task_started",
		},
		{
			name:     "updated",
			ev:       turnevent.BackgroundTaskUpdated{TaskID: "SECRETKINDZZZ", Patch: "SECRETKINDZZZ"},
			wantKind: "background_task_updated",
		},
		{
			name: "roster",
			ev: turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{
				{TaskID: "SECRETKINDZZZ", TaskType: "SECRETKINDZZZ", Description: "SECRETKINDZZZ"},
			}},
			wantKind: "background_task_roster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			cur := &stubCursor{} // empty cursor: the no_cursor drop logs eventKind
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(cur, bcast, logger)

			e.Handle(context.Background(), tt.ev)

			logs := buf.String()
			if logs == "" {
				t.Fatal("expected a DEBUG no-cursor drop log; got none")
			}
			if !strings.Contains(logs, "kind="+tt.wantKind) {
				t.Fatalf("log does not name the variant (want kind=%s):\n%s", tt.wantKind, logs)
			}
			if strings.Contains(logs, "kind=unknown") {
				t.Fatalf("eventKind returned unknown for %s:\n%s", tt.wantKind, logs)
			}
			if strings.Contains(logs, "SECRETKINDZZZ") {
				t.Fatalf("claude-derived text leaked into the kind log:\n%s", logs)
			}
		})
	}
}

// AC#1: a turnevent.ThinkingProgress reaches a mobile client as a
// thinking_progress frame carrying conversation identity and both of the event's
// integer readings — and only a phone that negotiated `interactive` receives it.
// The two readings differ in the fixture so a handler that wired one field to
// both wire keys goes red here.
func TestInteractiveTurnEmitterV2_ThinkingProgressFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ThinkingProgress{EstimatedTokens: 184, EstimatedTokensDelta: 37})

	if got := len(bcast.pushes); got != 1 {
		t.Fatalf("pushed %d envelopes; want exactly 1 (interactive conn only)", got)
	}
	p := bcast.pushes[0]
	if p.connID != "a" {
		t.Fatalf("pushed to conn %q; want interactive conn %q", p.connID, "a")
	}
	if p.env.Type != protocol.TypeThinkingProgress {
		t.Fatalf("envelope type: got %q, want %q", p.env.Type, protocol.TypeThinkingProgress)
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatalf("non-interactive conn b received the %s frame", protocol.TypeThinkingProgress)
	}

	var got protocol.ThinkingProgressPayload
	if err := json.Unmarshal(p.env.Payload, &got); err != nil {
		t.Fatalf("decode thinking_progress payload: %v", err)
	}
	want := protocol.ThinkingProgressPayload{
		ConversationID:       testConvID,
		EstimatedTokens:      184,
		EstimatedTokensDelta: 37,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload:\n got %#v\nwant %#v", got, want)
	}
	// The wire literal is the daemon's own name for the event, never claude's
	// system/thinking_tokens subtype nor anything derived from it. Asserted on
	// the envelope BYTES reaching the conn, which a constant splice cannot
	// launder; internal/protocol's TestThinkingProgressType_IsNotClaudesSubtype
	// pins the constant itself.
	if bytes.Contains([]byte(p.env.Type), []byte("tokens")) {
		t.Fatalf("wire type %q is derived from claude's subtype", p.env.Type)
	}
	if p.env.Type != "thinking_progress" {
		t.Fatalf("wire type literal: got %q, want %q", p.env.Type, "thinking_progress")
	}
}

// AC#2: a thinking-progress frame opens and closes no turn. It is handled bare
// before any turn and emits only its own frame — no turn_state, no turn_end —
// and the tracker's inTurn/turnID/currentState are asserted directly, then a
// following content event is driven through to prove a fresh turn still opens.
func TestInteractiveTurnEmitterV2_ThinkingProgressNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ThinkingProgress{EstimatedTokens: 5, EstimatedTokensDelta: 5})

	// A turn_state anywhere in the sequence is the observable signature of a
	// transitionTo call, so the exact single-frame sequence is the assertion.
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeThinkingProgress}) {
		t.Fatalf("bare thinking_progress envelopes: got %v, want [%s]", got, protocol.TypeThinkingProgress)
	}
	if e.inTurn {
		t.Error("thinking_progress opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("thinking_progress minted a turn id: got %q, want empty", e.turnID)
	}
	if e.currentState != "" {
		t.Errorf("thinking_progress set currentState: got %q, want empty", e.currentState)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeThinkingProgress,
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-thinking_progress envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after thinking_progress: got %v, want [responding]", got)
	}
}

// AC#2: mid-turn, buffered assistant text keeps its wire position AHEAD of the
// frame, and the open turn survives the interleave untouched.
//
// The load-bearing part is the event driven PAST the frame. A frame-local check
// passes even if the handler called endTurn, because the damage only shows on
// the NEXT event, when a fresh turn gets minted — the same unpinned-guard shape
// #1385 hit. So the second delta's turn_id and seq are what actually bite here.
func TestInteractiveTurnEmitterV2_ThinkingProgressMidTurnDoesNotDisturbOpenTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "a1"}) // opens turn, buffers a1
	beforeTurnID, beforeState := e.turnID, e.currentState
	if !e.inTurn {
		t.Fatal("precondition: a turn must be open before the interleave")
	}

	e.Handle(context.Background(), turnevent.ThinkingProgress{EstimatedTokens: 184, EstimatedTokensDelta: 37})

	if !e.inTurn {
		t.Error("thinking_progress closed the open turn; inTurn must stay true")
	}
	if e.turnID != beforeTurnID {
		t.Errorf("thinking_progress changed turnID: got %q, want %q", e.turnID, beforeTurnID)
	}
	if e.currentState != beforeState {
		t.Errorf("thinking_progress changed currentState: got %q, want %q", e.currentState, beforeState)
	}

	// Drive one event past the frame: this is what catches an endTurn.
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "a2"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,        // responding
		protocol.TypeAssistantDelta,   // a1, flushed AHEAD of the frame
		protocol.TypeThinkingProgress, // no surrounding turn_state
		protocol.TypeAssistantDelta,   // a2, flushed by turn_end
		protocol.TypeTurnEnd,          //
		protocol.TypeTurnState,        // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn thinking_progress envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].TurnID != beforeTurnID || deltas[1].TurnID != beforeTurnID {
		t.Fatalf("thinking_progress split the turn: %q, %q want both %q", deltas[0].TurnID, deltas[1].TurnID, beforeTurnID)
	}
	if deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("thinking_progress disrupted seq: got %d,%d want 0,1", deltas[0].Seq, deltas[1].Seq)
	}
}

// AC#3: a turn producing no ThinkingProgress produces no such frames, so an
// ordinary turn gains no traffic. The EXACT pushed sequence is asserted, not
// merely "no thinking_progress present" — an emit accidentally made
// unconditional shows up as an extra frame anywhere in the sequence, which an
// absence check placed on the wrong property would miss.
func TestInteractiveTurnEmitterV2_OrdinaryTurnEmitsNoThinkingProgressFrames(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.ThoughtChunk{Text: "reasoning"},
		turnevent.TextChunk{Text: "hello"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "Bash", RawInput: json.RawMessage(`{"command":"ls"}`)},
		turnevent.ToolUpdate{ToolCallID: "t1", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "ok"}},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	// Unchanged from main: a thinking turn's frames are exactly these six.
	wantTypes := []string{
		protocol.TypeTurnState,      // thinking
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // hello
		protocol.TypeToolUse,        // Bash
		protocol.TypeToolResult,     // ok
		protocol.TypeTurnEnd,        // end_turn
		protocol.TypeTurnState,      // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("ordinary-turn envelope order:\n got %v\nwant %v", got, wantTypes)
	}
}

// eventKind's thinking-progress arm is live code and content-free. The arm is
// NOT for this emitter's default (the handler case above claims the variant
// first) but for eventKind's other call sites — acp_turn_stream.go,
// stream_turn_busy.go, stream_turn_drain.go — where the ACP surface drops the
// variant via acpbridge's own default and logs the kind. Without the arm those
// logs read kind=unknown for a variant the daemon does recognize. The
// empty-cursor drop is the reachable eventKind call site on this lane.
func TestInteractiveTurnEmitterV2_ThinkingProgressEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Drop slog's own time= attr, for the reason measured on this very test
		// and recorded above TestInteractiveTurnEmitterV2_RateLimitedEventKindNamesTheVariant.
		// This test is the one that carried the hazard: its needles below are bare
		// numerals, so the timestamp's digits matched them. With the attr gone the
		// whole record — the message, event=interactive_turn.no_cursor and
		// kind=thinking_progress — carries no digit at all, so either needle can
		// now match only a genuine leak.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	cur := &stubCursor{} // empty cursor: the no_cursor drop logs eventKind
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), turnevent.ThinkingProgress{EstimatedTokens: 184, EstimatedTokensDelta: 37})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=thinking_progress") {
		t.Fatalf("log does not name the variant (want kind=thinking_progress):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for thinking_progress:\n%s", logs)
	}
	// slog's own time= is the digit source that made this test's numeric needles
	// flaky; the readings check below is only sound while it is absent. Asserted
	// rather than merely configured, because dropping the ReplaceAttr above leaves
	// the readings check passing on most runs — the disarming has to be red on
	// EVERY run, not on the ~5% where the clock happens to spell a needle.
	if strings.Contains(logs, "time=") {
		t.Fatalf("capture carries slog's timestamp; the readings check below can match the clock:\n%s", logs)
	}
	// Both readings are claude's own integers; neither ever reaches a log.
	for _, reading := range []string{"184", "37"} {
		if strings.Contains(logs, reading) {
			t.Fatalf("claude-authored reading %q leaked into the kind log:\n%s", reading, logs)
		}
	}
}

// The claude-authored fixture values the three rate-limited tests below share.
// They are conspicuous sentinels rather than natural-looking values on purpose:
// the AC#3 test asserts its negative with a strings.Contains over the whole
// captured log, and that log carries the literal kind=rate_limited. A natural
// Status of "limited", "limit", "rate" or "rate_limited" is a substring of it, so
// the negative would be RED against a correct implementation. Every value here is
// therefore a substring of neither the frame name, nor the log's own event name
// and message, nor any other value — and none contains a character encoding/json
// escapes, so the byte assertions in internal/turnbridge can be written literally.
const (
	rateLimitedStatusFixture    = "qq-status-sentinel"
	rateLimitedLimitTypeFixture = "zz-limittype-sentinel"
	rateLimitedResetsAtFixture  = 4102444800 // 2100-01-01Z: not a plausible instant
)

var rateLimitedTruncatedFixture = []string{"tf-alpha-sentinel", "tf-beta-sentinel"}

// AC#2: a rate-limited frame opens and closes no turn. It is handled bare before
// any turn and emits only its own frame — no turn_state, no turn_end — and the
// tracker's inTurn/turnID/currentState are asserted directly, then a following
// content event is driven through to prove a fresh turn still opens.
func TestInteractiveTurnEmitterV2_RateLimitedNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.RateLimited{
		Status:          rateLimitedStatusFixture,
		LimitType:       rateLimitedLimitTypeFixture,
		ResetsAt:        rateLimitedResetsAtFixture,
		TruncatedFields: rateLimitedTruncatedFixture,
	})

	// A turn_state anywhere in the sequence is the observable signature of a
	// transitionTo call, so the exact single-frame sequence is the assertion.
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeRateLimited}) {
		t.Fatalf("bare rate_limited envelopes: got %v, want [%s]", got, protocol.TypeRateLimited)
	}
	if e.inTurn {
		t.Error("rate_limited opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("rate_limited minted a turn id: got %q, want empty", e.turnID)
	}
	if e.currentState != "" {
		t.Errorf("rate_limited set currentState: got %q, want empty", e.currentState)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeRateLimited,
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-rate_limited envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after rate_limited: got %v, want [responding]", got)
	}
}

// AC#2: mid-turn, buffered assistant text keeps its wire position AHEAD of the
// frame, and the open turn survives the interleave untouched.
//
// The load-bearing part is the event driven PAST the frame. A frame-local check
// passes even if the handler called endTurn, because an endTurn on an already-open
// turn only shows its damage on the NEXT event, when a fresh turn gets minted. So
// the second delta's turn_id and seq are what actually bite here.
func TestInteractiveTurnEmitterV2_RateLimitedMidTurnDoesNotDisturbOpenTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "a1"}) // opens turn, buffers a1
	beforeTurnID, beforeState := e.turnID, e.currentState
	if !e.inTurn {
		t.Fatal("precondition: a turn must be open before the interleave")
	}

	e.Handle(context.Background(), turnevent.RateLimited{
		Status:          rateLimitedStatusFixture,
		LimitType:       rateLimitedLimitTypeFixture,
		ResetsAt:        rateLimitedResetsAtFixture,
		TruncatedFields: rateLimitedTruncatedFixture,
	})

	if !e.inTurn {
		t.Error("rate_limited closed the open turn; inTurn must stay true")
	}
	if e.turnID != beforeTurnID {
		t.Errorf("rate_limited changed turnID: got %q, want %q", e.turnID, beforeTurnID)
	}
	if e.currentState != beforeState {
		t.Errorf("rate_limited changed currentState: got %q, want %q", e.currentState, beforeState)
	}

	// Drive one event past the frame: this is what catches an endTurn.
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "a2"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // a1, flushed AHEAD of the frame
		protocol.TypeRateLimited,    // no surrounding turn_state
		protocol.TypeAssistantDelta, // a2, flushed by turn_end
		protocol.TypeTurnEnd,        //
		protocol.TypeTurnState,      // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn rate_limited envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].TurnID != beforeTurnID || deltas[1].TurnID != beforeTurnID {
		t.Fatalf("rate_limited split the turn: %q, %q want both %q", deltas[0].TurnID, deltas[1].TurnID, beforeTurnID)
	}
	if deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("rate_limited disrupted seq: got %d,%d want 0,1", deltas[0].Seq, deltas[1].Seq)
	}
}

// AC#3: eventKind's rate-limited arm is live code and content-free. The arm
// (#1404) shipped untested; this is the test. It exists for eventKind's OTHER
// call sites — acp_turn_stream.go, stream_turn_busy.go, stream_turn_drain.go —
// where the variant is dropped and the kind logged; without the arm those logs
// read kind=unknown for a variant the daemon does recognize. The empty-cursor
// drop is the reachable eventKind call site on this lane.
//
// The negative is the half that discriminates. Status is precisely the field a
// log line wants to explain itself with, and an arm returning "rate_limited:" +
// Status leaves strings.Contains(logs, "kind=rate_limited") TRUE — so the
// positive assertion alone does not catch it and the per-value negative does.
func TestInteractiveTurnEmitterV2_RateLimitedEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Drop slog's own time= attr. The negative below is a strings.Contains
		// over the WHOLE captured log, and the timestamp is a host-dependent
		// source of digits a numeric needle can collide with — measured on the
		// ThinkingProgress test above, whose "37" needle hits the timestamp on
		// ~5% of runs and on 100% of runs landing in any minute :37 or any
		// second :37, and whose safety for a "-1"-shaped needle depends on the
		// host's UTC offset. That is a scheduled flake, not a rare one: against
		// 2026-08-25T00:12:31.378+03:00 the needle matches when the minute is 37
		// (1/60), when the second is 37 (1/60), or when the millisecond field
		// contains it (~1.9%). An earlier ~2% figure recorded here undercounted,
		// because it was taken inside a single -count=N burst — one burst shares
		// a minute and usually a second, so it structurally cannot observe the
		// two 1/60 terms. Removing the attr deletes the false-positive source
		// outright rather than choosing needles around it.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	cur := &stubCursor{} // empty cursor: the no_cursor drop logs eventKind
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), turnevent.RateLimited{
		Status:          rateLimitedStatusFixture,
		LimitType:       rateLimitedLimitTypeFixture,
		ResetsAt:        rateLimitedResetsAtFixture,
		TruncatedFields: rateLimitedTruncatedFixture,
	})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=rate_limited") {
		t.Fatalf("log does not name the variant (want kind=rate_limited):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for rate_limited:\n%s", logs)
	}
	// Every claude-authored value on the event, including the instant: none may
	// reach a log, whether as a kind suffix or as a stray field.
	leakable := append([]string{
		rateLimitedStatusFixture,
		rateLimitedLimitTypeFixture,
		strconv.FormatInt(rateLimitedResetsAtFixture, 10),
	}, rateLimitedTruncatedFixture...)
	for _, value := range leakable {
		if strings.Contains(logs, value) {
			t.Fatalf("claude-authored value %q leaked into the kind log:\n%s", value, logs)
		}
	}
}

// modelAnnouncedFixture is the claude-authored model value the eventKind test
// below drives. A conspicuous sentinel rather than a realistic identifier, for
// rateLimitedStatusFixture's reason: the negative is a strings.Contains over the
// WHOLE captured log, which carries the literal kind=model_announced, so a natural
// value like "model" or "announced" would be a substring of the log's own text and
// the negative would be RED against a correct implementation.
const modelAnnouncedFixture = "ZZMODELSENTINELZZ"

// #1600 AC4: eventKind's model-announced arm is live code and content-free.
//
// Model is precisely the field the #833 posture — restated across
// internal/relay's v2session_settings.go and internal/sessions' pool.go as "model
// / effort / YOLO values are NEVER logged at any level" — exists to keep out of
// logs, so the negative is the half that discriminates: an arm returning
// "model_announced:" + Model leaves strings.Contains(logs, "kind=model_announced")
// TRUE, and only the per-value check catches it.
//
// The arm exists for eventKind's call sites rather than for this lane's Handle
// case: the variant now HAS a Handle arm (#1638), and this test reaches eventKind
// only because its cursor is empty, so Handle returns at the no-cursor guard
// before the type switch. acp_turn_stream.go, stream_turn_busy.go and
// stream_turn_drain.go log the kind too. Without the arm every one of them reads
// kind=unknown for a variant the daemon does recognize. The empty-cursor drop is
// the reachable eventKind call site on this lane — more precisely so since #1638,
// because with a LIVE cursor the event is claimed by the Handle arm and reaches no
// eventKind site at all. Hence the empty cursor below is load-bearing, not
// incidental: it is what keeps this test's assertion reachable.
func TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Drop slog's own time= attr, for the rate-limited test's measured reason: a
		// whole-log strings.Contains has a host-dependent source of digits to collide
		// with otherwise.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	cur := &stubCursor{} // empty cursor: the no_cursor drop logs eventKind
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), turnevent.ModelAnnounced{Model: modelAnnouncedFixture, Truncated: true})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=model_announced") {
		t.Fatalf("log does not name the variant (want kind=model_announced):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for model_announced:\n%s", logs)
	}
	if strings.Contains(logs, modelAnnouncedFixture) {
		t.Fatalf("claude's announced model leaked into the kind log:\n%s", logs)
	}
}

// #1638 AC#2: a model-announced frame opens and closes no turn. It is handled
// bare before any turn and emits exactly one frame — no turn_state, no turn_end —
// and the tracker's inTurn/turnID/currentState are asserted directly, then a
// following content event is driven through to prove a fresh turn still opens.
//
// The lifecycle answer matters more here than it does for its rate-limited
// template: this frame arrives once per turn in EVERY conversation, so a
// startTurnIfNeeded in the arm would wedge all of them rather than an unlucky one.
//
// modelAnnouncedFixture is reused rather than redeclared — it is already this
// file's conspicuous sentinel for the variant, and reading it here changes nothing
// about the eventKind test that owns it.
func TestInteractiveTurnEmitterV2_ModelAnnouncedNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ModelAnnounced{
		Model:     modelAnnouncedFixture,
		Truncated: true,
	})

	// A turn_state anywhere in the sequence is the observable signature of a
	// transitionTo call, so the exact single-frame sequence is the assertion — and
	// it is also AC#2's "an event arriving with no turn open still emits exactly
	// one frame".
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeModelAnnounced}) {
		t.Fatalf("bare model_announced envelopes: got %v, want [%s]", got, protocol.TypeModelAnnounced)
	}
	if e.inTurn {
		t.Error("model_announced opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("model_announced minted a turn id: got %q, want empty", e.turnID)
	}
	if e.currentState != "" {
		t.Errorf("model_announced set currentState: got %q, want empty", e.currentState)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeModelAnnounced,
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-model_announced envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after model_announced: got %v, want [responding]", got)
	}
}

// #1638 AC#2: mid-turn, buffered assistant text keeps its wire position AHEAD of
// the frame, and the open turn survives the interleave untouched.
//
// The load-bearing part is the event driven PAST the frame. A frame-local check
// passes even if the handler called endTurn, because an endTurn on an already-open
// turn only shows its damage on the NEXT event, when a fresh turn gets minted. So
// the second delta's turn_id and seq are what actually bite here.
func TestInteractiveTurnEmitterV2_ModelAnnouncedMidTurnDoesNotDisturbOpenTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "a1"}) // opens turn, buffers a1
	beforeTurnID, beforeState := e.turnID, e.currentState
	if !e.inTurn {
		t.Fatal("precondition: a turn must be open before the interleave")
	}

	e.Handle(context.Background(), turnevent.ModelAnnounced{
		Model:     modelAnnouncedFixture,
		Truncated: true,
	})

	if !e.inTurn {
		t.Error("model_announced closed the open turn; inTurn must stay true")
	}
	if e.turnID != beforeTurnID {
		t.Errorf("model_announced changed turnID: got %q, want %q", e.turnID, beforeTurnID)
	}
	if e.currentState != beforeState {
		t.Errorf("model_announced changed currentState: got %q, want %q", e.currentState, beforeState)
	}

	// Drive one event past the frame: this is what catches an endTurn.
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "a2"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // a1, flushed AHEAD of the frame
		protocol.TypeModelAnnounced, // no surrounding turn_state
		protocol.TypeAssistantDelta, // a2, flushed by turn_end
		protocol.TypeTurnEnd,        //
		protocol.TypeTurnState,      // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn model_announced envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].TurnID != beforeTurnID || deltas[1].TurnID != beforeTurnID {
		t.Fatalf("model_announced split the turn: %q, %q want both %q", deltas[0].TurnID, deltas[1].TurnID, beforeTurnID)
	}
	if deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("model_announced disrupted seq: got %d,%d want 0,1", deltas[0].Seq, deltas[1].Seq)
	}
}

// emitterModelListDropped is the entry count claude sent beyond streamsup's
// maxModelListEntries. Conspicuously non-zero so the wire assertion below cannot
// pass against a mapping that hard-codes 0 or recomputes len(Models), and a value
// whose decimal spelling appears nowhere in the captured log of the eventKind test
// (which drops slog's time attr, leaving a record with no digits at all).
const emitterModelListDropped = 41

// emitterModelListFixture is the claude-authored inventory every model_list test drives.
// Package-level and READ-ONLY: the tests below run in parallel and share it, and
// the carry-never-mutate rule turnbridge.MapEvent's ModelList arm states means
// nothing on this lane may write through those slice headers anyway.
//
// Conspicuous sentinels rather than realistic identifiers, for
// modelAnnouncedFixture's reason one variant over: the eventKind negative is a
// strings.Contains over the WHOLE captured log, which carries the literal
// kind=model_list and the event name interactive_turn.no_cursor, so a natural value
// containing "model", "list", "event" or "announced" would be a substring of the
// log's own text and the negative would be RED against a correct implementation.
//
// The two entries are deliberately DISTINGUISHABLE in every field
// (turnbridge-package.md's own rule: identical rows let a swapped-index bug pass),
// and their TruncatedFields differ on purpose — the second is nil, which is the
// load-bearing absence protocol.ModelOption.MarshalJSON exempts from its nil→[]
// normalisation while normalising EffortLevels. A test that "fixed" that asymmetry
// would be asserting the opposite of the wire contract.
var emitterModelListFixture = turnevent.ModelList{
	Models: []turnevent.ModelOption{
		{
			ResolvedModel:    "qq-resolved-alpha-sentinel",
			Value:            "qq-value-alpha-sentinel",
			DisplayName:      "qq-display-alpha-sentinel",
			EffortLevels:     []string{"qq-effort-alpha-sentinel"},
			SupportsAutoMode: true,
			TruncatedFields:  []string{"qq-truncated-alpha-sentinel"},
		},
		{
			ResolvedModel:    "zz-resolved-beta-sentinel",
			Value:            "zz-value-beta-sentinel",
			DisplayName:      "zz-display-beta-sentinel",
			EffortLevels:     []string{"zz-effort-beta-sentinel", "zz-effort-gamma-sentinel"},
			SupportsAutoMode: false,
			TruncatedFields:  nil,
		},
	},
	DroppedModels: emitterModelListDropped,
}

// emitterModelListSentinels returns every claude-authored string emitterModelListFixture
// carries, for the log-leak negatives. Derived from the fixture rather than
// re-listed beside it, so a sentinel added to an entry cannot silently drop out of
// the assertions.
func emitterModelListSentinels() []string {
	var out []string
	for _, m := range emitterModelListFixture.Models {
		out = append(out, m.ResolvedModel, m.Value, m.DisplayName)
		out = append(out, m.EffortLevels...)
		out = append(out, m.TruncatedFields...)
	}
	return out
}

// #1849 AC#1: a model list reaches every interactive conn as one model_list
// envelope carrying exactly what turnbridge.MapEvent produced for the event.
//
// This is the ONLY test in the family that decodes payload fields; the rest assert
// on pushTypes. The per-field want below is built from the fixture's own fields
// rather than from re-typed literals, which keeps it honest about a swapped
// assignment inside MapEvent's loop: every field of every entry holds a distinct
// sentinel, so a Value/DisplayName swap or an entry-index swap is red.
func TestInteractiveTurnEmitterV2_ModelListFansOutToEveryInteractiveConn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: true},
		{ConnID: "c", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), emitterModelListFixture)

	for _, connID := range []string{"a", "b"} {
		got := pushesFor(bcast.pushes, connID)
		if len(got) != 1 {
			t.Fatalf("conn %s received %d envelopes; want exactly 1", connID, len(got))
		}
		if got[0].env.Type != protocol.TypeModelList {
			t.Fatalf("conn %s envelope type: got %q, want %q", connID, got[0].env.Type, protocol.TypeModelList)
		}
	}
	if got := pushesFor(bcast.pushes, "c"); len(got) != 0 {
		t.Fatalf("non-interactive conn received %d envelopes; want 0", len(got))
	}

	var pl protocol.ModelListPayload
	if err := json.Unmarshal(pushesFor(bcast.pushes, "a")[0].env.Payload, &pl); err != nil {
		t.Fatalf("decode model_list payload: %v", err)
	}
	if pl.ConversationID != testConvID {
		t.Errorf("conversation_id: got %q, want %q", pl.ConversationID, testConvID)
	}
	if pl.DroppedModels != emitterModelListDropped {
		t.Errorf("dropped_models: got %d, want %d", pl.DroppedModels, emitterModelListDropped)
	}
	src := emitterModelListFixture.Models
	want := []protocol.ModelOption{
		{
			ResolvedModel:    src[0].ResolvedModel,
			Value:            src[0].Value,
			DisplayName:      src[0].DisplayName,
			EffortLevels:     src[0].EffortLevels,
			SupportsAutoMode: src[0].SupportsAutoMode,
			TruncatedFields:  src[0].TruncatedFields,
		},
		{
			ResolvedModel:    src[1].ResolvedModel,
			Value:            src[1].Value,
			DisplayName:      src[1].DisplayName,
			EffortLevels:     src[1].EffortLevels,
			SupportsAutoMode: src[1].SupportsAutoMode,
			// nil, and it stays nil through the round trip: ModelOption.MarshalJSON
			// exempts this field from the nil -> [] normalisation it applies to
			// EffortLevels, so nothing-was-cut reaches the wire as null.
			TruncatedFields: src[1].TruncatedFields,
		},
	}
	if !reflect.DeepEqual(pl.Models, want) {
		t.Fatalf("model_list rows on the wire:\n got %+v\nwant %+v", pl.Models, want)
	}
}

// #1849 AC#2: a model_list frame opens and closes no turn. It is handled bare
// before any turn and emits exactly one frame — no turn_state, no turn_end — and
// the tracker's inTurn/turnID/currentState are asserted directly, then a following
// content event is driven through to prove a fresh turn still opens.
//
// The lifecycle answer is the one TestTurnMarkFor_TotalOverEveryVariant already
// pins for this variant as turnMarkNone; this is the emitter agreeing with it. It
// matters here for a reason one step further out than model_announced's: the list
// is a property of the CHILD, reported once per initialize exchange, so a turn
// opened on one has no turn end anywhere in its future to clear it.
func TestInteractiveTurnEmitterV2_ModelListNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), emitterModelListFixture)

	// A turn_state anywhere in the sequence is the observable signature of a
	// transitionTo call, so the exact single-frame sequence is the assertion.
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeModelList}) {
		t.Fatalf("bare model_list envelopes: got %v, want [%s]", got, protocol.TypeModelList)
	}
	if e.inTurn {
		t.Error("model_list opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("model_list minted a turn id: got %q, want empty", e.turnID)
	}
	if e.currentState != "" {
		t.Errorf("model_list set currentState: got %q, want empty", e.currentState)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeModelList,
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-model_list envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after model_list: got %v, want [responding]", got)
	}
}

// #1849 AC#2: mid-turn, buffered assistant text keeps its wire position AHEAD of
// the frame, and the open turn survives the interleave untouched.
//
// The load-bearing part is the event driven PAST the frame. A frame-local check
// passes even if the handler called endTurn, because an endTurn on an already-open
// turn only shows its damage on the NEXT event, when a fresh turn gets minted. So
// the second delta's turn_id and the unbroken seq are what actually bite here.
func TestInteractiveTurnEmitterV2_ModelListMidTurnDoesNotDisturbOpenTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "a1"}) // opens turn, buffers a1
	beforeTurnID, beforeState := e.turnID, e.currentState
	if !e.inTurn {
		t.Fatal("precondition: a turn must be open before the interleave")
	}

	e.Handle(context.Background(), emitterModelListFixture)

	if !e.inTurn {
		t.Error("model_list closed the open turn; inTurn must stay true")
	}
	if e.turnID != beforeTurnID {
		t.Errorf("model_list changed turnID: got %q, want %q", e.turnID, beforeTurnID)
	}
	if e.currentState != beforeState {
		t.Errorf("model_list changed currentState: got %q, want %q", e.currentState, beforeState)
	}

	// Drive one event past the frame: this is what catches an endTurn.
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "a2"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // a1, flushed AHEAD of the frame
		protocol.TypeModelList,      // no surrounding turn_state
		protocol.TypeAssistantDelta, // a2, flushed by turn_end
		protocol.TypeTurnEnd,        //
		protocol.TypeTurnState,      // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn model_list envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].TurnID != beforeTurnID || deltas[1].TurnID != beforeTurnID {
		t.Fatalf("model_list split the turn: %q, %q want both %q", deltas[0].TurnID, deltas[1].TurnID, beforeTurnID)
	}
	if deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("model_list disrupted seq: got %d,%d want 0,1", deltas[0].Seq, deltas[1].Seq)
	}
}

// #1849 AC#3 + AC#5: a list arriving before any conversation has been routed
// produces no envelope and no turn, and the drop that does fire names the variant
// and nothing else.
//
// The empty cursor is load-bearing rather than incidental, exactly as it is in the
// model_announced test above: with a LIVE cursor the Handle arm claims the event
// and it reaches no eventKind call site at all, so the no-cursor drop is what keeps
// this assertion reachable on this lane. The arm exists for the OTHER call sites
// too — acp_turn_stream.go, stream_turn_busy.go, stream_turn_drain.go — which would
// otherwise read kind=unknown for a variant the daemon does recognize.
//
// The negatives are the half that discriminates, and this variant multiplies the
// temptation rather than merely repeating it: every entry carries a Value, a
// ResolvedModel, a DisplayName and an effort list, and both counts are derived from
// the list's contents. An arm returning "model_list:" + Value, or + the entry
// count, leaves strings.Contains(logs, "kind=model_list") TRUE — so the positive
// assertion alone passes it and only the per-value checks catch it.
func TestInteractiveTurnEmitterV2_ModelListEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Drop slog's own time= attr, for the rate-limited test's measured reason: a
		// whole-log strings.Contains has a host-dependent source of digits to collide
		// with otherwise. That is what makes the two numeric needles below safe — the
		// remaining record carries no digits at all.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	cur := &stubCursor{} // empty cursor: no conversation has been routed yet
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), emitterModelListFixture)

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=model_list") {
		t.Fatalf("log does not name the variant (want kind=model_list):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for model_list:\n%s", logs)
	}
	leakable := append(emitterModelListSentinels(),
		strconv.Itoa(emitterModelListDropped),
		strconv.Itoa(len(emitterModelListFixture.Models)),
	)
	for _, value := range leakable {
		if strings.Contains(logs, value) {
			t.Fatalf("value derived from the model list %q leaked into the kind log:\n%s", value, logs)
		}
	}

	// AC#3's other half on the same rig: the pre-routing drop is unchanged, so no
	// frame reaches the wire and no turn is opened on the way to the drop.
	if len(bcast.pushes) != 0 {
		t.Fatalf("pre-routing model_list pushed %d envelopes; want 0", len(bcast.pushes))
	}
	if e.inTurn {
		t.Error("pre-routing model_list opened a turn; inTurn must stay false")
	}
}

// #1849 AC#4: a session whose child never reported a list gets no model_list frame
// at any point in a turn — the arm emits only in response to an actual event.
//
// Not a tautology. turnbridge.MapEvent maps a ZERO-VALUE ModelList rather than
// dropping it ("whether the event exists at all is the producer's" gate), so an
// emitter that synthesised a frame at a turn boundary, on a lifecycle hook, or from
// a retained-but-empty list would put an empty menu on a phone's wire and this is
// the test that reddens.
func TestInteractiveTurnEmitterV2_ModelListNotSynthesizedWithoutAnEvent(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{MessageID: "m1", Text: "a1"},
		turnevent.ToolStart{ToolCallID: "t1", Title: "grep"},
		turnevent.ToolUpdate{ToolCallID: "t1", Status: turnevent.ToolStatusCompleted},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(context.Background(), ev)
	}

	if got := pushTypes(bcast.pushes); slices.Contains(got, protocol.TypeModelList) {
		t.Fatalf("an ordinary turn emitted a %s frame with no ModelList event: %v", protocol.TypeModelList, got)
	}
}

// emitterSlashCommandListFixture is the workspace-authored inventory every
// slash_command_list test drives. Package-level and READ-ONLY: the tests below run
// in parallel and share it, and turnevent.SlashCommandList's carried-never-mutated
// rule means nothing on this lane may write through that slice header anyway.
//
// Conspicuous sentinels rather than realistic command names, for
// emitterModelListFixture's reason: the eventKind negative is a strings.Contains
// over the WHOLE captured log, which carries the literal kind=slash_command_list
// and the event names interactive_turn.no_cursor / interactive_turn.unknown, so a
// natural value — clear, compact, or anything containing "slash", "command",
// "list", "turn", "event", "cursor", "drop" or "kind" — would be a substring of the
// log's own text and the negative would be RED against a CORRECT implementation.
//
// The two entries are deliberately DISTINGUISHABLE in every field
// (turnbridge-package.md's own rule: identical rows let a swapped-index bug pass),
// and their TruncatedFields differ on purpose — the second is nil, the family's
// load-bearing absence, present in the fixture from the start rather than added by
// whichever slice first needs it.
var emitterSlashCommandListFixture = turnevent.SlashCommandList{
	Commands: []turnevent.SlashCommand{
		{
			Name:            "qq-name-alpha-sentinel",
			ArgumentHint:    "qq-hint-alpha-sentinel",
			Description:     "qq-description-alpha-sentinel",
			TruncatedFields: []string{"qq-truncated-alpha-sentinel"},
		},
		{
			Name:            "zz-name-beta-sentinel",
			ArgumentHint:    "zz-hint-beta-sentinel",
			Description:     "zz-description-beta-sentinel",
			TruncatedFields: nil,
		},
	},
}

// emitterSlashCommandListSentinels returns every workspace-authored string
// emitterSlashCommandListFixture carries, for the log-leak negatives. Derived from
// the fixture rather than re-listed beside it, so a sentinel added to an ENTRY
// cannot silently drop out of the assertions.
//
// WHAT DERIVATION DOES NOT BUY, corrected in #1904 because this doc claimed it did:
// the enumeration does NOT grow with turnevent.SlashCommand's FIELD set. The body
// names fields one at a time, so a new field is simply not enumerated and its value
// gets no log-leak assertion at all — and this lane is where "no log line carries
// decoded content" is a deterministic test rather than an advisory sentence. Each
// field-adding slice therefore owes a sentinel on every entry and a line here;
// Description is the first one to pay it, ArgumentHint (#1957) the second, and the
// first of them is the proof the claim needed correcting.
//
// EVERY ENTRY'S HINT IS NON-EMPTY, and that is a REQUIREMENT of these assertions
// rather than a stylistic choice, because the needles are fed to strings.Contains
// and strings.Contains(s, "") is ALWAYS TRUE. An entry carrying the field's ordinary
// captured shape — 33 of the committed capture's 51 hints are "" — would make the
// negative below fail against a CORRECT implementation. The repair is a non-empty
// sentinel on every entry and NOT a filter that skips empty needles here: a filter
// makes the sweep green again while silently disarming leak detection for every
// future field whose fixture value happens to be empty, which is the advisory
// posture this lane exists to replace.
func emitterSlashCommandListSentinels() []string {
	var out []string
	for _, c := range emitterSlashCommandListFixture.Commands {
		out = append(out, c.Name)
		out = append(out, c.ArgumentHint)
		out = append(out, c.Description)
		out = append(out, c.TruncatedFields...)
	}
	return out
}

// slashCommandListDropLogger returns a DEBUG logger capturing into buf with slog's
// own time attr dropped, for the model_list test's measured reason: a whole-log
// strings.Contains has a host-dependent source of digits to collide with
// otherwise. That is what makes the numeric needle in the assertions below safe —
// the remaining record carries no digits at all.
func slashCommandListDropLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// assertSlashCommandListKindLeaksNothing is the shared half of the two drop-site
// tests: the captured log names the variant, does not read as unknown, and carries
// nothing derived from the event — no entry's Name, no entry's TruncatedFields,
// and not the entry count.
//
// The negatives are the half that discriminates. An arm returning
// "slash_command_list:" + Name, or + len(Commands), leaves
// strings.Contains(logs, "kind=slash_command_list") TRUE, so the positive
// assertion alone passes it and only the per-value checks catch it.
func assertSlashCommandListKindLeaksNothing(t *testing.T, logs string) {
	t.Helper()

	if logs == "" {
		t.Fatal("expected a DEBUG drop log; got none")
	}
	if !strings.Contains(logs, "kind=slash_command_list") {
		t.Fatalf("log does not name the variant (want kind=slash_command_list):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for slash_command_list:\n%s", logs)
	}
	leakable := append(emitterSlashCommandListSentinels(),
		strconv.Itoa(len(emitterSlashCommandListFixture.Commands)),
	)
	for _, value := range leakable {
		if strings.Contains(logs, value) {
			t.Fatalf("value derived from the slash-command list %q leaked into the kind log:\n%s", value, logs)
		}
	}
}

// #1854 AC#3: the drop-logging kind function names the variant and returns the
// variant NAME only.
//
// The empty cursor is load-bearing rather than incidental, exactly as it is in the
// model_list test above: it makes Handle's no-cursor drop — which returns before
// the type switch — a reachable eventKind call site. The arm exists for the other
// live sites too: Handle's default arm (see the live-cursor test below), and
// stream_turn_drain.go's sinkFor sink-full drop and startStreamTurnDrainV2
// not-active-session drop, all of which would otherwise read kind=unknown for a
// variant the daemon does recognize.
//
// The discipline matters MORE for this variant than for the model list rather than
// less: every string it carries is WORKSPACE-authored, a lower-trust origin than
// claude's own strings, so the #833 posture that keeps model values out of a log
// covers these a fortiori.
func TestInteractiveTurnEmitterV2_SlashCommandListEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cur := &stubCursor{} // empty cursor: no conversation has been routed yet
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, slashCommandListDropLogger(&buf))

	e.Handle(context.Background(), emitterSlashCommandListFixture)

	assertSlashCommandListKindLeaksNothing(t, buf.String())

	// The pre-routing drop is unchanged, so no frame reaches the wire and no turn is
	// opened on the way to the drop.
	if len(bcast.pushes) != 0 {
		t.Fatalf("pre-routing slash_command_list pushed %d envelopes; want 0", len(bcast.pushes))
	}
	if e.inTurn {
		t.Error("pre-routing slash_command_list opened a turn; inTurn must stay false")
	}
}

// #1854's scope boundary, pinned deterministically: with a conversation routed the
// event reaches Handle's DEFAULT arm — no case claims it — so nothing in this slice
// produces or publishes the variant.
//
// This is a deliberate tripwire. The slice that finally gives Handle an arm for
// this variant WILL redden it, and updating it is that slice's work — the same
// lifecycle every arm-claiming ticket in this family has had.
func TestInteractiveTurnEmitterV2_SlashCommandListIsNotPublished(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, slashCommandListDropLogger(&buf))

	e.Handle(context.Background(), emitterSlashCommandListFixture)

	assertSlashCommandListKindLeaksNothing(t, buf.String())

	if len(bcast.pushes) != 0 {
		t.Fatalf("slash_command_list pushed %d envelopes; want 0 — nothing in this slice publishes it", len(bcast.pushes))
	}
	if e.inTurn {
		t.Error("slash_command_list opened a turn; inTurn must stay false")
	}
}
