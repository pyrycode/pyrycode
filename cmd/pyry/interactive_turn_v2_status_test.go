package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

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

// A turnevent.ThinkingProgress reaches a mobile client after its opening
// turn_state as a thinking_progress frame carrying conversation identity and both
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

	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeTurnState, protocol.TypeThinkingProgress}) {
		t.Fatalf("envelope order = %v, want [turn_state thinking_progress]", got)
	}
	progress := pushesOfType(bcast.pushes, protocol.TypeThinkingProgress)
	if len(progress) != 1 {
		t.Fatalf("thinking_progress count = %d, want 1", len(progress))
	}
	p := progress[0]
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

// A thinking-progress frame observed before assistant content opens the turn and
// publishes thinking before the numeric reading. Its terminal result returns the
// same lifecycle to idle.
func TestInteractiveTurnEmitterV2_ThinkingProgressStartsTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ThinkingProgress{EstimatedTokens: 5, EstimatedTokensDelta: 5})

	wantOpenTypes := []string{protocol.TypeTurnState, protocol.TypeThinkingProgress}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantOpenTypes) {
		t.Fatalf("bare thinking_progress envelopes: got %v, want %v", got, wantOpenTypes)
	}
	if !e.inTurn {
		t.Fatal("thinking_progress left the emitter outside a turn")
	}
	if e.turnID == "" {
		t.Error("thinking_progress did not mint a turn id")
	}
	if e.currentState != "thinking" {
		t.Errorf("currentState = %q, want %q", e.currentState, "thinking")
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"thinking"}) {
		t.Fatalf("turn_state after thinking_progress: got %v, want [thinking]", got)
	}

	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	wantTypes := []string{
		protocol.TypeTurnState, // thinking opens the turn
		protocol.TypeThinkingProgress,
		protocol.TypeTurnEnd,
		protocol.TypeTurnState, // idle closes it
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("thinking_progress lifecycle envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"thinking", "idle"}) {
		t.Fatalf("turn_state lifecycle: got %v, want [thinking idle]", got)
	}
	if e.inTurn || e.turnID != "" || e.currentState != "" {
		t.Fatalf("terminal result left emitter open: inTurn=%v turnID=%q state=%q", e.inTurn, e.turnID, e.currentState)
	}
}

// Mid-turn, buffered assistant text keeps its wire position ahead of the frame,
// the phase moves responding→thinking→responding, and the turn identity survives.
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
	beforeTurnID := e.turnID
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
	if e.currentState != "thinking" {
		t.Errorf("thinking_progress state: got %q, want %q", e.currentState, "thinking")
	}

	// Drive one event past the frame: this is what catches an endTurn.
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "a2"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // a1, flushed AHEAD of the frame
		protocol.TypeTurnState,      // thinking
		protocol.TypeThinkingProgress,
		protocol.TypeTurnState,      // responding again
		protocol.TypeAssistantDelta, // a2, flushed by turn_end
		protocol.TypeTurnEnd,        //
		protocol.TypeTurnState,      // idle
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
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding", "thinking", "responding", "idle"}) {
		t.Fatalf("turn_state interleave: got %v, want [responding thinking responding idle]", got)
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
// first) but for eventKind's other call sites — stream_turn_busy.go and
// stream_turn_drain.go — which drop the variant and log the kind. Without the
// arm those logs read kind=unknown for a variant the daemon does recognize. The
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

// TestInteractiveTurnEmitterV2_CompactionBoundaryEventKindNamesTheVariant is
// #2237's half of the arm above's claim, and its content-free negative is the one
// that matters: Trigger is claude's own word for what started the compaction, and it
// is precisely the field a log line explaining a compaction would reach for.
//
// The trigger fixture is a conspicuous sentinel rather than "manual", for the reason
// the rate-limited block below states: the captured log carries the literal
// kind=compaction_boundary, so a natural value that is a substring of the frame name
// or the event name would make the negative red against a correct implementation.
func TestInteractiveTurnEmitterV2_CompactionBoundaryEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Drop slog's own time= attr: the count needles below are bare numerals and
		// would otherwise match the clock's digits, exactly as measured on the
		// thinking-progress test above.
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

	pre, post := 23600, 2612
	e.Handle(context.Background(), turnevent.CompactionBoundary{
		Trigger:    compactionBoundaryTriggerFixture,
		PreTokens:  &pre,
		PostTokens: &post,
	})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=compaction_boundary") {
		t.Fatalf("log does not name the variant (want kind=compaction_boundary):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for compaction_boundary:\n%s", logs)
	}
	if strings.Contains(logs, "time=") {
		t.Fatalf("capture carries slog's timestamp; the count checks below can match the clock:\n%s", logs)
	}
	if strings.Contains(logs, compactionBoundaryTriggerFixture) {
		t.Fatalf("claude-authored trigger %q leaked into the kind log:\n%s",
			compactionBoundaryTriggerFixture, logs)
	}
	// Both counts are claude's own integers; neither ever reaches a log.
	for _, count := range []string{"23600", "2612"} {
		if strings.Contains(logs, count) {
			t.Fatalf("claude-authored count %q leaked into the kind log:\n%s", count, logs)
		}
	}
}

// compactionBoundaryTriggerFixture is a sentinel for the reason the rate-limited
// block below gives: a substring of the frame name, the event name or the log
// message would make a content-free negative red against correct code.
const compactionBoundaryTriggerFixture = "qq-trigger-sentinel"

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
// call sites — stream_turn_busy.go and stream_turn_drain.go — where the
// variant is dropped and the kind logged; without the arm those logs read
// kind=unknown for a variant the daemon does recognize. The empty-cursor
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

// The banner fixtures are conspicuous sentinels rather than natural values, for the
// reason the compaction-boundary block above states: the eventKind capture carries the
// literal kind=banner and event=interactive_turn.no_cursor, so a natural value that is a
// substring of either would make the leak negatives red against a correct
// implementation. Both are digit-free, which is what makes the clock a non-issue here —
// the numeric-needle hazard measured on the thinking-progress test cannot arise.
const (
	bannerTextFixture  = "OPERATOR-TEXT-SENTINEL"
	bannerLevelFixture = "LEVEL-SENTINEL"
)

// TestInteractiveTurnEmitterV2_BannerReachesClientWithNoLifecycleMutation is #2256's
// AC 2 at the production path, and the ARRIVAL half is what makes it load-bearing.
// Both switches this ticket touches default silently, so a missing Handle arm drops the
// frame with nothing red anywhere — no compile error, no failing sibling. Asserting the
// envelope reaches a connected client is the only assertion that dies when the arm is
// gone; asserting the arm compiles would not.
//
// The values are read from the DECODED PUSH rather than from a log buffer, following
// the session-facts test above: the eventKind test below forbids either string from
// reaching a log at all, so an assertion that found them in log text would be passing
// on a leak.
func TestInteractiveTurnEmitterV2_BannerReachesClientWithNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.Banner{
		Level:     bannerLevelFixture,
		Text:      bannerTextFixture,
		Truncated: true,
		StopsTurn: true,
	})

	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeBanner}) {
		t.Fatalf("banner envelopes:\n got %v\nwant %v — a turn_state here would be a "+
			"lifecycle mutation, and an empty sequence means the Handle arm is gone",
			got, []string{protocol.TypeBanner})
	}
	var pl protocol.BannerPayload
	if err := json.Unmarshal(pushesFor(bcast.pushes, "a")[0].env.Payload, &pl); err != nil {
		t.Fatalf("decode banner payload: %v", err)
	}
	if pl.ConversationID != testConvID {
		t.Errorf("conversation_id: got %q, want %q", pl.ConversationID, testConvID)
	}
	if pl.Level != bannerLevelFixture {
		t.Errorf("level: got %q, want %q", pl.Level, bannerLevelFixture)
	}
	if pl.Text != bannerTextFixture {
		t.Errorf("text: got %q, want %q", pl.Text, bannerTextFixture)
	}
	if !pl.Truncated {
		t.Error("truncated: got false, want the producer's true")
	}
	if !pl.StopsTurn {
		t.Error("stops_turn: got false, want the producer's true")
	}
	// The lifecycle half. StopsTurn is true above precisely so this is a real check: a
	// daemon that ever read that field as a lever would close the turn here, and a
	// banner arriving with no turn open would then wedge the conversation.
	if e.inTurn {
		t.Error("banner opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("banner minted a turn id %q; it must mint none", e.turnID)
	}
}

// TestInteractiveTurnEmitterV2_BannerFlushesPendingDeltaFirst pins the ordering half of
// the arm: buffered assistant text keeps its wire position AHEAD of the banner, so a
// client renders what claude said before the notice about the session. Without the
// flushDelta call the delta would surface later, behind a frame that came after it.
func TestInteractiveTurnEmitterV2_BannerFlushesPendingDeltaFirst(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "hello"})
	e.Handle(context.Background(), turnevent.Banner{Level: bannerLevelFixture, Text: bannerTextFixture})

	got := pushTypes(bcast.pushes)
	if len(got) < 2 {
		t.Fatalf("envelopes: got %v, want a flushed delta followed by a banner", got)
	}
	if last := got[len(got)-1]; last != protocol.TypeBanner {
		t.Fatalf("last envelope: got %q, want %q — the banner must arrive after the "+
			"text it followed:\n%v", last, protocol.TypeBanner, got)
	}
	if before := got[len(got)-2]; before != protocol.TypeAssistantDelta {
		t.Fatalf("envelope before the banner: got %q, want %q — the pending delta must be "+
			"flushed immediately ahead of it:\n%v", before, protocol.TypeAssistantDelta, got)
	}
}

// TestInteractiveTurnEmitterV2_BannerEventKindNamesTheVariant is the arm's other half,
// and its content-free negative is the widest on this switch. Text is arbitrary
// claude-authored prose whose whole purpose is to explain something to an operator, so
// a log line explaining a banner would reach for it first; Level reads like a log level
// by construction. Neither may reach a log field.
func TestInteractiveTurnEmitterV2_BannerEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cur := &stubCursor{} // empty cursor: the no_cursor drop logs eventKind
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), turnevent.Banner{
		Level:     bannerLevelFixture,
		Text:      bannerTextFixture,
		Truncated: true,
		StopsTurn: true,
	})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=banner") {
		t.Fatalf("log does not name the variant (want kind=banner):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for banner:\n%s", logs)
	}
	// No ReplaceAttr dropping slog's time= here, unlike the two tests above, and the
	// reason is that both needles are digit-free sentinels: the clock cannot spell
	// either, so the hazard those tests disarm does not exist on this one.
	for _, secret := range []string{bannerTextFixture, bannerLevelFixture} {
		if strings.Contains(logs, secret) {
			t.Fatalf("claude-authored value %q leaked into the kind log:\n%s", secret, logs)
		}
	}
}
