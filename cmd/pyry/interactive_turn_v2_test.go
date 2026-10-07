package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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

// pushesOfType is pushesFor's sibling on the other axis: it selects by envelope type
// rather than by conn, for a test whose arm emits one frame among several and needs to
// address a specific one without depending on the emission order.
func pushesOfType(pushes []recordedPush, typ string) []recordedPush {
	var out []recordedPush
	for _, p := range pushes {
		if p.env.Type == typ {
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
		// #2003 AC#3 one inventory further, and the trust origin is what makes it a
		// tightening rather than a repeat: every string this one carries is
		// WORKSPACE-authored — a command defined in a repository was written by
		// whoever wrote that repository — so the posture covers it a fortiori. Same
		// log-heavy rig, same payload-presence pairing at the bottom. This is where
		// the EMIT-path half of AC#3 lives; the drop-path half is
		// assertSlashCommandListKindLeaksNothing's, which cannot be aimed here
		// because it Fatals on an empty log and this path writes no drop record.
		emitterSlashCommandListFixture,
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
	// NO COUNT NEEDLE HERE, and its absence is deliberate rather than an omission
	// from AC#3's "no entry count". This rig keeps slog's time attr AND every
	// push_err record carries env_id, a small monotonic integer, so a whole-log
	// strings.Contains for a two-digit-or-shorter count is TRUE against a correct
	// implementation — the vacuous-needle failure one polarity over. The count
	// negative therefore lives where the record is digit-free: the drop-path
	// helper, whose logger drops the time attr and whose records carry no env_id.
	// Do not "complete" the sweep by adding it back here.
	leakable = append(leakable, emitterSlashCommandListSentinels()...)
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

	// The same polarity pair for the slash-command list (#2003), and it is the half
	// that makes this test AC#3's emit-path home rather than a second log negative:
	// the sentinels are required ABSENT from the log above and PRESENT on the wire
	// here, so a Handle arm that quietly dropped the event — which passes every
	// log-absence check on its own — is red.
	var invoked *protocol.SlashCommandListPayload
	for _, p := range bcast.pushes {
		if p.env.Type != protocol.TypeSlashCommandList {
			continue
		}
		var pl protocol.SlashCommandListPayload
		if err := json.Unmarshal(p.env.Payload, &pl); err != nil {
			t.Fatalf("decode slash_command_list payload: %v", err)
		}
		invoked = &pl
		break
	}
	if invoked == nil {
		t.Fatalf("no %s envelope reached the wire; the Handle arm dropped the event", protocol.TypeSlashCommandList)
	}
	if len(invoked.Commands) != len(emitterSlashCommandListFixture.Commands) {
		t.Fatalf("slash_command_list rows on the wire: got %d, want %d",
			len(invoked.Commands), len(emitterSlashCommandListFixture.Commands))
	}
	if invoked.Commands[0].Name != emitterSlashCommandListFixture.Commands[0].Name {
		t.Fatalf("first row's name on the wire: got %q, want %q",
			invoked.Commands[0].Name, emitterSlashCommandListFixture.Commands[0].Name)
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

// AC-1: every fanned-out envelope carries its durable, daemon-wide-unique event id
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

// AC#1/#2/#3: a ToolCallDenied fans out as one tool_denied envelope to
// interactive-capable conns only, carrying the cursor's conversation_id, the live
// turn's id, and a tool_use_id BYTE-IDENTICAL to the tool_use frame for the same call.
//
// The denial is handled after a ToolStart because that is the observed line order
// (assistant/tool_use → system/permission_denied → user/tool_result), and it is what
// lets the join be asserted against a real sibling frame rather than against a literal
// this test wrote twice.
func TestInteractiveTurnEmitterV2_ToolDeniedFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	ctx := context.Background()
	e.Handle(ctx, turnevent.ToolStart{
		ToolCallID: "toolu_01A9F",
		Title:      "Bash",
		Kind:       turnevent.ToolKindExecute,
		RawInput:   json.RawMessage(`{"command":"ls /etc"}`),
	})
	e.Handle(ctx, turnevent.ToolCallDenied{
		ToolName:        "Bash",
		ToolCallID:      "toolu_01A9F",
		Message:         "requested permissions to use Bash",
		TruncatedFields: []string{"message"},
	})

	denials := pushesOfType(bcast.pushes, protocol.TypeToolDenied)
	if len(denials) != 1 {
		t.Fatalf("tool_denied pushed %d envelopes; want exactly 1 (interactive conn only)", len(denials))
	}
	if denials[0].connID != "a" {
		t.Fatalf("tool_denied pushed to conn %q; want interactive conn %q", denials[0].connID, "a")
	}
	var dp protocol.ToolDeniedPayload
	if err := json.Unmarshal(denials[0].env.Payload, &dp); err != nil {
		t.Fatalf("decode tool_denied payload: %v", err)
	}
	if dp.ConversationID != testConvID {
		t.Fatalf("tool_denied conversation_id: got %q, want %q", dp.ConversationID, testConvID)
	}
	if dp.ToolName != "Bash" || dp.Message != "requested permissions to use Bash" {
		t.Fatalf("tool_denied payload: got %+v", dp)
	}
	if want := []string{"message"}; !slices.Equal(dp.TruncatedFields, want) {
		t.Fatalf("tool_denied truncated_fields: got %v, want %v", dp.TruncatedFields, want)
	}
	if dp.DroppedFields != nil {
		t.Fatalf("tool_denied dropped_fields: got %v, want nil", dp.DroppedFields)
	}

	uses := pushesOfType(bcast.pushes, protocol.TypeToolUse)
	if len(uses) != 1 {
		t.Fatalf("expected exactly one tool_use to join against; got %d", len(uses))
	}
	var up protocol.ToolUsePayload
	if err := json.Unmarshal(uses[0].env.Payload, &up); err != nil {
		t.Fatalf("decode tool_use payload: %v", err)
	}
	// The join key, and the turn the two share. A denial that opened a turn of its own
	// would leave the client unable to place the row beside the call it names.
	if dp.ToolUseID != up.ToolUseID {
		t.Fatalf("tool_denied tool_use_id %q != tool_use tool_use_id %q; the two frames "+
			"describe the same call and a client joins them on this value",
			dp.ToolUseID, up.ToolUseID)
	}
	if dp.TurnID == "" || dp.TurnID != up.TurnID {
		t.Fatalf("tool_denied turn_id %q != tool_use turn_id %q", dp.TurnID, up.TurnID)
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatal("non-interactive conn b received the tool_denied")
	}
}

// TestInteractiveTurnEmitterV2_ToolDeniedEventKindNamesTheVariant is the log half of
// the arm above's claim, and its content-free negative is the one that matters here:
// the tool name and claude's rejection prose are exactly what a log line explaining a
// denial would reach for, and the prose may quote the command line that was refused.
//
// The fixtures are conspicuous sentinels rather than "Bash" and a natural sentence, on
// the reason the compaction-boundary and rate-limited blocks above state: the captured
// log carries the literal kind=tool_denied, so a natural value that is a substring of
// the frame name or the event name would make the negative red against correct code.
func TestInteractiveTurnEmitterV2_ToolDeniedEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cur := &stubCursor{} // empty cursor: the no_cursor drop logs eventKind
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), turnevent.ToolCallDenied{
		ToolName:           toolDeniedNameFixture,
		ToolCallID:         toolDeniedIDFixture,
		Message:            toolDeniedMessageFixture,
		DecisionReasonType: toolDeniedReasonTypeFixture,
		DecisionReason:     toolDeniedReasonFixture,
	})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=tool_denied") {
		t.Fatalf("log does not name the variant (want kind=tool_denied):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for tool_denied:\n%s", logs)
	}
	// Every claude-derived field on the variant, including the id: none reaches a log.
	for _, leak := range []string{
		toolDeniedNameFixture, toolDeniedIDFixture, toolDeniedMessageFixture,
		toolDeniedReasonTypeFixture, toolDeniedReasonFixture,
	} {
		if strings.Contains(logs, leak) {
			t.Fatalf("claude-authored value %q leaked into the kind log:\n%s", leak, logs)
		}
	}
}

// A ToolProgress has no wire arm until #2324, but every turn lane must still
// recognize its kind. The no-cursor path reaches eventKind and also proves that
// neither Claude-authored field is copied into the log.
func TestInteractiveTurnEmitterV2_ToolProgressEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	const id = "SECRETTOOLPROGRESSIDZZZ"
	const elapsed = 987654321
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cur := &stubCursor{}
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)
	e.Handle(context.Background(), turnevent.ToolProgress{ToolCallID: id, ElapsedSeconds: elapsed})

	logs := buf.String()
	if !strings.Contains(logs, "kind=tool_progress") {
		t.Fatalf("log does not name the variant (want kind=tool_progress):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for tool_progress:\n%s", logs)
	}
	for _, leak := range []string{id, strconv.Itoa(elapsed)} {
		if strings.Contains(logs, leak) {
			t.Fatalf("claude-authored value %q leaked into the kind log:\n%s", leak, logs)
		}
	}
}

// The claude-authored fixture values the tool-denied kind test uses. Conspicuous
// sentinels rather than natural values, for the reason the compaction-boundary block
// above gives: a substring of the frame name, the event name or the log message would
// make a content-free negative red against correct code.
const (
	toolDeniedNameFixture       = "qq-toolname-sentinel"
	toolDeniedIDFixture         = "qq-toolid-sentinel"
	toolDeniedMessageFixture    = "qq-message-sentinel"
	toolDeniedReasonTypeFixture = "qq-reasontype-sentinel"
	toolDeniedReasonFixture     = "qq-reason-sentinel"
)

func TestInteractiveTurnEmitterV2_ToolProgressFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()

	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	ctx := context.Background()
	e.Handle(ctx, turnevent.ToolStart{ToolCallID: "tool-progress-33", Title: "Bash"})
	e.Handle(ctx, turnevent.ToolProgress{ToolCallID: "tool-progress-33", ElapsedSeconds: -44})

	progress := pushesOfType(bcast.pushes, protocol.TypeToolProgress)
	if len(progress) != 1 {
		t.Fatalf("tool_progress pushed %d envelopes; want exactly 1", len(progress))
	}
	if progress[0].connID != "a" {
		t.Fatalf("tool_progress pushed to conn %q, want interactive conn a", progress[0].connID)
	}
	var got protocol.ToolProgressPayload
	if err := json.Unmarshal(progress[0].env.Payload, &got); err != nil {
		t.Fatalf("decode tool_progress payload: %v", err)
	}
	uses := pushesOfType(bcast.pushes, protocol.TypeToolUse)
	if len(uses) != 1 {
		t.Fatalf("tool_use count: got %d, want 1", len(uses))
	}
	var use protocol.ToolUsePayload
	if err := json.Unmarshal(uses[0].env.Payload, &use); err != nil {
		t.Fatalf("decode tool_use payload: %v", err)
	}
	if got.ConversationID != testConvID {
		t.Errorf("ConversationID: got %q, want %q", got.ConversationID, testConvID)
	}
	if got.TurnID == "" || got.TurnID != use.TurnID {
		t.Errorf("TurnID: got %q, want tool_use turn %q", got.TurnID, use.TurnID)
	}
	if got.ToolUseID != use.ToolUseID {
		t.Errorf("ToolUseID: got %q, want tool_use id %q", got.ToolUseID, use.ToolUseID)
	}
	if got.ElapsedSeconds != -44 {
		t.Errorf("ElapsedSeconds: got %d, want -44 verbatim", got.ElapsedSeconds)
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatal("non-interactive conn b received tool_progress")
	}
}
