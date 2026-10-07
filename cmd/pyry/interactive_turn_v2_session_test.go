package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

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
// before the type switch. stream_turn_busy.go and stream_turn_drain.go log the
// kind too. Without the arm every one of them reads
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

func TestInteractiveTurnEmitterV2_ModelRefusalFallbackFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "interactive", Interactive: true},
		{ConnID: "legacy", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	ctx := context.Background()
	e.Handle(ctx, turnevent.TextChunk{MessageID: "message-1", Text: "partial"})
	beforeTurnID, beforeState := e.turnID, e.currentState
	e.Handle(ctx, turnevent.ModelRefusalFallback{
		Scope:              "session",
		OriginalModel:      "original-sentinel",
		FallbackModel:      "fallback-sentinel",
		RefusalCategory:    "category-sentinel",
		RefusalExplanation: "excluded-explanation-sentinel",
		Banner:             "banner-sentinel",
		TruncatedFields:    []string{"refusal_explanation", "banner"},
		DroppedFields:      []string{"scope"},
	})

	if got := pushTypes(pushesFor(bcast.pushes, "interactive")); !slices.Equal(got, []string{
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta,
		protocol.TypeModelRefusalFallback,
	}) {
		t.Fatalf("interactive frame order: got %v", got)
	}
	if got := len(pushesFor(bcast.pushes, "legacy")); got != 0 {
		t.Fatalf("non-interactive connection received %d frames, want 0", got)
	}
	refusals := pushesOfType(bcast.pushes, protocol.TypeModelRefusalFallback)
	if len(refusals) != 1 {
		t.Fatalf("model_refusal_fallback pushed %d envelopes, want exactly 1", len(refusals))
	}
	var payload protocol.ModelRefusalFallbackPayload
	if err := json.Unmarshal(refusals[0].env.Payload, &payload); err != nil {
		t.Fatalf("decode model_refusal_fallback payload: %v", err)
	}
	if payload.ConversationID != testConvID || payload.OriginalModel != "original-sentinel" ||
		payload.FallbackModel != "fallback-sentinel" || payload.Scope != "session" ||
		payload.RefusalCategory != "category-sentinel" || payload.Banner != "banner-sentinel" {
		t.Fatalf("model_refusal_fallback payload: got %+v", payload)
	}
	if want := []string{"banner"}; !slices.Equal(payload.TruncatedFields, want) {
		t.Errorf("truncated_fields: got %v, want %v", payload.TruncatedFields, want)
	}
	if want := []string{"scope"}; !slices.Equal(payload.DroppedFields, want) {
		t.Errorf("dropped_fields: got %v, want %v", payload.DroppedFields, want)
	}
	if !e.inTurn || e.turnID != beforeTurnID || e.currentState != beforeState {
		t.Errorf("refusal fallback mutated lifecycle: inTurn=%v turnID=%q state=%q; want true, %q, %q",
			e.inTurn, e.turnID, e.currentState, beforeTurnID, beforeState)
	}
}

func TestInteractiveTurnEmitterV2_ModelRefusalFallbackEventKindIsContentFree(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newInteractiveTurnEmitterV2(&stubCursor{}, &fakeInteractiveBcast{}, logger)
	values := []string{
		"ORIGINAL-MODEL-SENTINEL", "FALLBACK-MODEL-SENTINEL", "SCOPE-SENTINEL",
		"CATEGORY-SENTINEL", "BANNER-SENTINEL", "REPORT-SENTINEL",
	}
	e.Handle(context.Background(), turnevent.ModelRefusalFallback{
		OriginalModel:   values[0],
		FallbackModel:   values[1],
		Scope:           values[2],
		RefusalCategory: values[3],
		Banner:          values[4],
		DroppedFields:   []string{values[5]},
	})

	logs := buf.String()
	if !strings.Contains(logs, "kind=model_refusal_fallback") {
		t.Fatalf("log does not name model_refusal_fallback: %s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown: %s", logs)
	}
	for _, value := range values {
		if strings.Contains(logs, value) {
			t.Fatalf("claude-authored value %q leaked into the kind log: %s", value, logs)
		}
	}
}

func TestInteractiveTurnEmitterV2_ModelRefusalNoFallbackFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "interactive", Interactive: true},
		{ConnID: "legacy", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	ctx := context.Background()
	e.Handle(ctx, turnevent.TextChunk{MessageID: "message-1", Text: "partial"})
	beforeTurnID, beforeState := e.turnID, e.currentState
	e.Handle(ctx, turnevent.ModelRefusalNoFallback{
		OriginalModel:      "original-sentinel",
		RefusalCategory:    "category-sentinel",
		RefusalExplanation: "excluded-explanation-sentinel",
		Banner:             "banner-sentinel",
		TruncatedFields:    []string{"refusal_explanation", "banner"},
		DroppedFields:      []string{"original_model"},
	})

	if got := pushTypes(pushesFor(bcast.pushes, "interactive")); !slices.Equal(got, []string{
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta,
		protocol.TypeModelRefusalNoFallback,
	}) {
		t.Fatalf("interactive frame order: got %v", got)
	}
	if got := len(pushesFor(bcast.pushes, "legacy")); got != 0 {
		t.Fatalf("non-interactive connection received %d frames, want 0", got)
	}
	refusals := pushesOfType(bcast.pushes, protocol.TypeModelRefusalNoFallback)
	if len(refusals) != 1 {
		t.Fatalf("model_refusal_no_fallback pushed %d envelopes, want exactly 1", len(refusals))
	}
	var payload protocol.ModelRefusalNoFallbackPayload
	if err := json.Unmarshal(refusals[0].env.Payload, &payload); err != nil {
		t.Fatalf("decode model_refusal_no_fallback payload: %v", err)
	}
	if payload.ConversationID != testConvID || payload.OriginalModel != "original-sentinel" ||
		payload.RefusalCategory != "category-sentinel" || payload.Banner != "banner-sentinel" {
		t.Fatalf("model_refusal_no_fallback payload: got %+v", payload)
	}
	if want := []string{"banner"}; !slices.Equal(payload.TruncatedFields, want) {
		t.Errorf("truncated_fields: got %v, want %v", payload.TruncatedFields, want)
	}
	if want := []string{"original_model"}; !slices.Equal(payload.DroppedFields, want) {
		t.Errorf("dropped_fields: got %v, want %v", payload.DroppedFields, want)
	}
	if !e.inTurn || e.turnID != beforeTurnID || e.currentState != beforeState {
		t.Errorf("refusal no-fallback mutated lifecycle: inTurn=%v turnID=%q state=%q; want true, %q, %q",
			e.inTurn, e.turnID, e.currentState, beforeTurnID, beforeState)
	}
}

func TestInteractiveTurnEmitterV2_ModelRefusalNoFallbackDoesNotOpenTurn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "interactive", Interactive: true},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ModelRefusalNoFallback{
		OriginalModel:   "original-sentinel",
		RefusalCategory: "category-sentinel",
		Banner:          "banner-sentinel",
	})

	if e.inTurn || e.turnID != "" || e.currentState != "" {
		t.Errorf("refusal no-fallback opened a turn: inTurn=%v turnID=%q state=%q",
			e.inTurn, e.turnID, e.currentState)
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeModelRefusalNoFallback}) {
		t.Fatalf("frames without an open turn: got %v, want only model_refusal_no_fallback", got)
	}
}

func TestInteractiveTurnEmitterV2_ModelRefusalNoFallbackEventKindIsContentFree(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newInteractiveTurnEmitterV2(&stubCursor{}, &fakeInteractiveBcast{}, logger)
	values := []string{
		"ORIGINAL-MODEL-NO-FALLBACK-SENTINEL", "CATEGORY-NO-FALLBACK-SENTINEL",
		"BANNER-NO-FALLBACK-SENTINEL", "REPORT-NO-FALLBACK-SENTINEL",
	}
	e.Handle(context.Background(), turnevent.ModelRefusalNoFallback{
		OriginalModel:   values[0],
		RefusalCategory: values[1],
		Banner:          values[2],
		DroppedFields:   []string{values[3]},
	})

	logs := buf.String()
	if !strings.Contains(logs, "kind=model_refusal_no_fallback") {
		t.Fatalf("log does not name model_refusal_no_fallback: %s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown: %s", logs)
	}
	for _, value := range values {
		if strings.Contains(logs, value) {
			t.Fatalf("claude-authored value %q leaked into the kind log: %s", value, logs)
		}
	}
}

// The two claude-authored values #2252 carries, as conspicuous sentinels. Distinct
// from each other and from modelAnnouncedFixture so a log sweep can say WHICH field
// leaked, and spelled so they collide with nothing slog itself writes.
const (
	sessionFactsVersionFixture = "ZZVERSIONSENTINELZZ"
	sessionFactsModeFixture    = "ZZMODESENTINELZZ"
)

// #2252 AC5: eventKind names the variant, and the name is ALL it returns.
//
// PermissionMode is precisely the field the #833 posture — restated across
// internal/relay's v2session_settings.go and internal/sessions' pool.go as "model /
// effort / YOLO values are NEVER logged at any level" — exists to keep out of logs,
// and it is the one a log line explaining an unexpected posture would reach for. So
// the negatives are the half that discriminates: an arm returning "session_facts:" +
// PermissionMode leaves the kind check TRUE and only the per-value checks catch it.
//
// The empty cursor is load-bearing rather than incidental, exactly as it is for the
// model_announced test above: the variant HAS a Handle arm (#2252), so with a live
// cursor it reaches eventKind through emitMapped's unmapped drop instead, and this
// test's assertion would then be about a different call site. Both are reachable
// today and both would read kind=unknown without the arm — as would
// stream_turn_busy.go and stream_turn_drain.go.
func TestInteractiveTurnEmitterV2_SessionFactsEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
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

	e.Handle(context.Background(), turnevent.SessionFacts{
		ClaudeCodeVersion: sessionFactsVersionFixture,
		PermissionMode:    sessionFactsModeFixture,
		TruncatedFields:   []string{"claude_code_version", "permission_mode"},
	})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG no-cursor drop log; got none")
	}
	if !strings.Contains(logs, "kind=session_facts") {
		t.Fatalf("log does not name the variant (want kind=session_facts):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for session_facts:\n%s", logs)
	}
	for _, leak := range []string{sessionFactsVersionFixture, sessionFactsModeFixture} {
		if strings.Contains(logs, leak) {
			t.Fatalf("claude-authored content (%q) leaked into the kind log:\n%s", leak, logs)
		}
	}
}

// #2252 AC5: the session-facts arm mutates no turn lifecycle. It is handled bare
// before any turn, the tracker's inTurn/turnID/currentState are asserted directly,
// and a following content event proves a fresh turn still opens.
//
// The lifecycle answer matters here for the model_announced test's reason and one
// step more: this rides the SAME per-turn init line, so it arrives once per turn in
// EVERY conversation, and a startTurnIfNeeded in the arm would wedge all of them.
//
// EXACTLY ONE FRAME IS PUSHED, and it is the report itself. #2252 wrote this
// assertion as "no frames at all" — accurate while turnbridge.MapEvent had no arm
// for the variant, and deliberately shaped to redden the moment one landed. #2254
// landed it, so the sequence now carries the frame and the assertion moved to its
// type and its wire values. The lifecycle claim did NOT weaken: a turn_state
// anywhere in the sequence is the observable signature of a transitionTo call, so
// requiring this one frame and no other still catches a lifecycle mutation, and the
// three tracker fields are asserted directly besides.
//
// The values are read from the DECODED PUSH rather than from a log buffer, which is
// not a stylistic choice: the eventKind test above forbids either string from
// reaching a log at all, so an assertion that found them in log text would be
// passing on a leak.
func TestInteractiveTurnEmitterV2_SessionFactsNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// A populated truncation report, so a mapping that dropped the field reddens here
	// as well as on the bytes in internal/turnbridge. Both members in one comparison
	// pins member ORDER, not merely membership.
	cut := []string{"claude_code_version", "permission_mode"}
	e.Handle(context.Background(), turnevent.SessionFacts{
		ClaudeCodeVersion: sessionFactsVersionFixture,
		PermissionMode:    sessionFactsModeFixture,
		TruncatedFields:   cut,
	})

	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeSessionFacts}) {
		t.Fatalf("session_facts envelopes:\n got %v\nwant %v — a turn_state here would be a "+
			"lifecycle mutation, and an empty sequence means the mapping arm is gone",
			got, []string{protocol.TypeSessionFacts})
	}
	var pl protocol.SessionFactsPayload
	if err := json.Unmarshal(pushesFor(bcast.pushes, "a")[0].env.Payload, &pl); err != nil {
		t.Fatalf("decode session_facts payload: %v", err)
	}
	if pl.ConversationID != testConvID {
		t.Errorf("conversation_id: got %q, want %q", pl.ConversationID, testConvID)
	}
	if pl.ClaudeCodeVersion != sessionFactsVersionFixture {
		t.Errorf("claude_code_version: got %q, want %q", pl.ClaudeCodeVersion, sessionFactsVersionFixture)
	}
	if pl.PermissionMode != sessionFactsModeFixture {
		t.Errorf("permission_mode: got %q, want %q", pl.PermissionMode, sessionFactsModeFixture)
	}
	if !slices.Equal(pl.TruncatedFields, cut) {
		t.Errorf("truncated_fields: got %v, want %v", pl.TruncatedFields, cut)
	}
	if e.inTurn {
		t.Error("session_facts opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("session_facts minted a turn id: got %q, want empty", e.turnID)
	}
	if e.currentState != "" {
		t.Errorf("session_facts set currentState: got %q, want empty", e.currentState)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeSessionFacts,   // the report, still first and still alone in its turn
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-session_facts envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after session_facts: got %v, want [responding]", got)
	}
}

var emitterMCPStatusFixture = turnevent.MCPStatus{
	Servers: []turnevent.MCPServerStatus{
		{
			Name:    "QQ-Zulu-Name-ZZ",
			Status:  "QQ-Zulu-Status-ZZ",
			Error:   "QQ-Zulu-Error-ZZ",
			Scope:   "QQ-Zulu-Scope-ZZ",
			Version: "QQ-Zulu-Version-ZZ",
		},
		{
			Name:    "QQ-Alpha-Name-ZZ",
			Status:  "QQ-Alpha-Status-ZZ",
			Error:   "QQ-Alpha-Error-ZZ",
			Scope:   "QQ-Alpha-Scope-ZZ",
			Version: "QQ-Alpha-Version-ZZ",
		},
	},
	DroppedServers: 7,
}

func TestInteractiveTurnEmitterV2_MCPStatusFansOutAndRecordsOnce(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: true},
		{ConnID: "c", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), emitterMCPStatusFixture)

	for _, connID := range []string{"a", "b"} {
		got := pushesFor(bcast.pushes, connID)
		if len(got) != 1 || got[0].env.Type != protocol.TypeMCPStatus {
			t.Fatalf("conn %s pushes = %#v, want one %s", connID, got, protocol.TypeMCPStatus)
		}
	}
	if got := pushesFor(bcast.pushes, "c"); len(got) != 0 {
		t.Fatalf("non-interactive conn received %d envelopes, want 0", len(got))
	}

	var payload protocol.MCPStatusPayload
	pushA := pushesFor(bcast.pushes, "a")[0].env
	if err := json.Unmarshal(pushA.Payload, &payload); err != nil {
		t.Fatalf("decode mcp_status: %v", err)
	}
	if payload.ConversationID != testConvID || payload.DroppedServers != emitterMCPStatusFixture.DroppedServers {
		t.Errorf("payload identity/count = (%q,%d), want (%q,%d)", payload.ConversationID,
			payload.DroppedServers, testConvID, emitterMCPStatusFixture.DroppedServers)
	}
	if len(payload.Servers) != 2 || payload.Servers[0].Name != "QQ-Zulu-Name-ZZ" ||
		payload.Servers[1].Name != "QQ-Alpha-Name-ZZ" {
		t.Fatalf("server order changed: %+v", payload.Servers)
	}

	ring, gap := e.ring.After(testConvID, 0)
	if gap || len(ring) != 1 {
		t.Fatalf("ring = %#v (gap=%v), want one logical event", ring, gap)
	}
	if ring[0].Type != protocol.TypeMCPStatus || !bytes.Equal(ring[0].Payload, pushA.Payload) {
		t.Fatalf("ring event differs from wire: ring=%s/%s wire=%s/%s",
			ring[0].Type, ring[0].Payload, pushA.Type, pushA.Payload)
	}
	if pushA.EventID == nil || *pushA.EventID != ring[0].ID {
		t.Fatalf("wire event id = %v, want ring id %d", pushA.EventID, ring[0].ID)
	}
}

func TestInteractiveTurnEmitterV2_EmptyMCPStatusIsLifecycleNeutral(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.MCPStatus{})

	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeMCPStatus}) {
		t.Fatalf("idle empty mcp_status envelopes = %v, want [%s]", got, protocol.TypeMCPStatus)
	}
	if e.inTurn || e.turnID != "" || e.currentState != "" {
		t.Fatalf("empty mcp_status changed lifecycle: inTurn=%v turnID=%q state=%q",
			e.inTurn, e.turnID, e.currentState)
	}
	var payload protocol.MCPStatusPayload
	if err := json.Unmarshal(bcast.pushes[0].env.Payload, &payload); err != nil {
		t.Fatalf("decode empty mcp_status: %v", err)
	}
	if payload.Servers == nil || len(payload.Servers) != 0 {
		t.Fatalf("empty servers = %#v, want non-nil empty slice from wire []", payload.Servers)
	}
}

func TestInteractiveTurnEmitterV2_MCPStatusPreservesMidTurnSequence(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "before"})
	turnID, state := e.turnID, e.currentState
	e.Handle(context.Background(), emitterMCPStatusFixture)
	if !e.inTurn || e.turnID != turnID || e.currentState != state {
		t.Fatalf("mcp_status disturbed turn: inTurn=%v turnID=%q/%q state=%q/%q",
			e.inTurn, e.turnID, turnID, e.currentState, state)
	}
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "after"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta,
		protocol.TypeMCPStatus,
		protocol.TypeAssistantDelta,
		protocol.TypeTurnEnd,
		protocol.TypeTurnState,
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn mcp_status order = %v, want %v", got, wantTypes)
	}
	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 || deltas[0].TurnID != turnID || deltas[1].TurnID != turnID ||
		deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("deltas around mcp_status = %+v, want same turn %q at seq 0,1", deltas, turnID)
	}
}

func TestInteractiveTurnEmitterV2_MCPStatusEventKindIsContentFree(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newInteractiveTurnEmitterV2(&stubCursor{}, &fakeInteractiveBcast{}, logger)

	e.Handle(context.Background(), emitterMCPStatusFixture)

	got := logs.String()
	if !strings.Contains(got, "kind=mcp_status") || strings.Contains(got, "kind=unknown") {
		t.Fatalf("no-cursor log does not name mcp_status safely: %s", got)
	}
	for _, server := range emitterMCPStatusFixture.Servers {
		for _, value := range []string{server.Name, server.Status, server.Error, server.Scope, server.Version} {
			if strings.Contains(got, value) {
				t.Fatalf("server value %q leaked into log: %s", value, got)
			}
		}
	}
}

// conversationResetFixture is #2134's conspicuous sentinel for the variant, and
// it is constrained twice over rather than merely chosen. It must satisfy
// transcript.ValidStem — the producer emits nothing otherwise — so unlike
// modelAnnouncedFixture's ZZ-delimited word it cannot be an arbitrary string; and
// it must share no substring with the captured log's own text, or the leak
// assertion below passes for the wrong reason. A full 36-character hex stem
// satisfies both: it is canonical, and no slog key, message, event name or kind
// value on this lane contains it.
//
// Deliberately NOT testConvID: that id is the DAEMON's conversation identity in
// these tests, and this event carries CLAUDE's. Reusing it would let a producer
// that emitted the wrong one of the two pass unnoticed.
const conversationResetFixture = "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0"

// TestInteractiveTurnEmitterV2_ConversationResetEventKindNamesTheVariant is
// #2134's AC4, and it is the INVERSE of its five siblings in this file rather
// than another copy of them. Each of those needs an EMPTY cursor to reach an
// eventKind call site at all, because a live cursor means the Handle arm claims
// the event before the type switch's default. This variant has no Handle arm, by
// design, so the cursor here is LIVE and the event lands in Handle's own default
// — which is the call site AC4 is actually about, and the first time since #2003
// that Debug is reachable for a variant a production producer emits.
//
// The live cursor is therefore load-bearing in the opposite direction to the
// ModelAnnounced test's empty one: swapping it for an empty cursor would still
// produce a log naming the variant, via the no-cursor drop, and would stop testing
// the thing that is new.
func TestInteractiveTurnEmitterV2_ConversationResetEventKindNamesTheVariant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Drop slog's own time= attr, for the rate-limited test's measured reason: a
		// whole-log strings.Contains has a host-dependent source of digits to collide
		// with otherwise — and this fixture is all digits and hex letters.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	cur := &stubCursor{}
	cur.set(testConvID) // LIVE cursor: the unknown-event drop in Handle's default logs eventKind
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, logger)

	e.Handle(context.Background(), turnevent.ConversationReset{NewConversationID: conversationResetFixture})

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected a DEBUG unknown-event drop log; got none")
	}
	// The drop this variant must take: Handle's default, not the no-cursor guard.
	// Asserted because the cursor being live is what makes this test different from
	// its siblings, and a regression that emptied it would otherwise stay green.
	if !strings.Contains(logs, "interactive_turn.unknown") {
		t.Errorf("want the unknown-event drop (interactive_turn.unknown), got:\n%s", logs)
	}
	if !strings.Contains(logs, "kind=conversation_reset") {
		t.Fatalf("log does not name the variant (want kind=conversation_reset):\n%s", logs)
	}
	if strings.Contains(logs, "kind=unknown") {
		t.Fatalf("eventKind returned unknown for conversation_reset:\n%s", logs)
	}
	if strings.Contains(logs, conversationResetFixture) {
		t.Fatalf("claude's new conversation id leaked into the kind log:\n%s", logs)
	}
}

// TestInteractiveTurnEmitterV2_ConversationResetEmitsNoFrame pins the other half
// of the variant's contract on this lane: it is DELIBERATELY unhandled, so it must
// emit nothing to a client and disturb no turn state. turnbridge.MapEvent's default
// drops it because the boundary a client draws comes from the session_transition
// frame, not from this event.
//
// The lifecycle answer is the one TestTurnMarkFor_TotalOverEveryVariant already
// records — turnMarkNone, neither opener nor closer — checked here at the emitter
// because a stray startTurnIfNeeded in a future Handle arm would wedge the OLD
// conversation, whose turn end belongs to a transcript claude has stopped writing.
func TestInteractiveTurnEmitterV2_ConversationResetEmitsNoFrame(t *testing.T) {
	t.Parallel()

	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ConversationReset{NewConversationID: conversationResetFixture})

	if got := len(bcast.pushes); got != 0 {
		t.Fatalf("frames pushed: got %d, want 0 — the variant owes no wire shape: %+v", got, bcast.pushes)
	}
	if e.inTurn {
		t.Error("a conversation_reset opened a turn; it is neither an opener nor a closer")
	}
}

// --- #2371: the turnevent.ContextUsage arm -----------------------------------

// emitterContextUsageFixture carries a DISTINCTIVE NEEDLE in every claude- and
// workspace-authored string, so the log-leak sweep below can look for each by name.
// The memory paths sit under the fictional /__pyry_fake__/memory/ root the
// fake-Claude canned reading uses, for the same reason: they cannot collide with a
// real path a log line might legitimately carry.
//
// The three dropped counts are MUTUALLY DISTINCT so a cross-wired pair reddens.
var emitterContextUsageFixture = turnevent.ContextUsage{
	Model:       "QQ-Zulu-Model-ZZ",
	TotalTokens: 9500,
	MaxTokens:   200000,
	Percentage:  5,
	Categories: []turnevent.ContextUsageCategory{
		{Name: "QQ-Zulu-Category-ZZ", Tokens: 4200},
		{Name: "QQ-Alpha-Category-ZZ", Tokens: 900},
	},
	DroppedCategories: 3,
	MCPTools: []turnevent.ContextUsageMCPTool{
		{Name: "QQ-Zulu-Tool-ZZ", ServerName: "QQ-Zulu-Server-ZZ", Tokens: 700},
	},
	DroppedMCPTools: 5,
	MemoryFiles: []turnevent.ContextUsageMemoryFile{
		{Path: "/__pyry_fake__/memory/QQ-Zulu-Path-ZZ.md", Type: "QQ-Zulu-Type-ZZ", Tokens: 31},
	},
	DroppedMemoryFiles: 7,
}

// contextUsageNeedles is every string of the fixture that MUST NOT reach a log
// record at any level (AC 5).
func contextUsageNeedles() []string {
	out := []string{emitterContextUsageFixture.Model}
	for _, c := range emitterContextUsageFixture.Categories {
		out = append(out, c.Name)
	}
	for _, tool := range emitterContextUsageFixture.MCPTools {
		out = append(out, tool.Name, tool.ServerName)
	}
	for _, file := range emitterContextUsageFixture.MemoryFiles {
		out = append(out, file.Path, file.Type)
	}
	return out
}

// TestInteractiveTurnEmitterV2_ContextUsageAfterTurnEndIsLifecycleNeutral is AC 2 at
// the event's REAL arrival order: the reading is solicited on TurnEnd, so it lands
// with the turn already closed. An arm that opened a turn here would mint one
// nothing will ever end — no later TurnEnd is coming to clear it.
func TestInteractiveTurnEmitterV2_ContextUsageAfterTurnEndIsLifecycleNeutral(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "hello"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	// The turn is closed here — this is the state the real event arrives in.
	inTurn, turnID, state := e.inTurn, e.turnID, e.currentState

	e.Handle(context.Background(), emitterContextUsageFixture)

	if e.inTurn != inTurn || e.turnID != turnID || e.currentState != state {
		t.Fatalf("context_usage mutated a CLOSED turn's lifecycle: inTurn=%v/%v turnID=%q/%q state=%q/%q",
			e.inTurn, inTurn, e.turnID, turnID, e.currentState, state)
	}
	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // hello
		protocol.TypeTurnEnd,
		protocol.TypeTurnState,    // idle
		protocol.TypeContextUsage, // AC 2: after that turn's turn_end
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-turn context_usage order:\n got %v\nwant %v — an extra turn_state here "+
			"is a re-minted turn no TurnEnd will ever close", got, wantTypes)
	}
}

// TestInteractiveTurnEmitterV2_ContextUsageIdleIsLifecycleNeutral is the same claim
// from the OTHER starting state: no turn has ever opened, so a lifecycle mutation
// shows up as a non-empty turnID rather than as an extra frame.
func TestInteractiveTurnEmitterV2_ContextUsageIdleIsLifecycleNeutral(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.ContextUsage{})

	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeContextUsage}) {
		t.Fatalf("idle empty context_usage envelopes = %v, want [%s] — an empty reading is a "+
			"POSITIVE report and must still publish", got, protocol.TypeContextUsage)
	}
	if e.inTurn || e.turnID != "" || e.currentState != "" {
		t.Fatalf("empty context_usage changed lifecycle: inTurn=%v turnID=%q state=%q",
			e.inTurn, e.turnID, e.currentState)
	}
	var payload protocol.ContextUsagePayload
	if err := json.Unmarshal(bcast.pushes[0].env.Payload, &payload); err != nil {
		t.Fatalf("decode empty context_usage: %v", err)
	}
	for _, list := range []struct {
		name string
		got  int
		nil_ bool
	}{
		{"categories", len(payload.Categories), payload.Categories == nil},
		{"mcp_tools", len(payload.MCPTools), payload.MCPTools == nil},
		{"memory_files", len(payload.MemoryFiles), payload.MemoryFiles == nil},
	} {
		if list.nil_ || list.got != 0 {
			t.Errorf("%s = len %d nil=%v, want non-nil empty slice decoded from wire []",
				list.name, list.got, list.nil_)
		}
	}
}

// TestInteractiveTurnEmitterV2_ContextUsageFansOutAndGatesCapability is AC 4: the
// frame rides the emitter's existing conversation-keyed fan-out, and the interactive
// capability is filtered ONCE inside emit. Conn "c" is non-interactive on the SAME
// conversation and must receive nothing.
func TestInteractiveTurnEmitterV2_ContextUsageFansOutAndGatesCapability(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: true},
		{ConnID: "c", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), emitterContextUsageFixture)

	for _, connID := range []string{"a", "b"} {
		got := pushesFor(bcast.pushes, connID)
		if len(got) != 1 || got[0].env.Type != protocol.TypeContextUsage {
			t.Fatalf("conn %s pushes = %#v, want one %s", connID, got, protocol.TypeContextUsage)
		}
	}
	if got := pushesFor(bcast.pushes, "c"); len(got) != 0 {
		t.Fatalf("non-interactive conn on the same conversation received %d envelopes, want 0 — "+
			"the capability gate lives in emit and must cover this frame like every other", len(got))
	}

	pushA := pushesFor(bcast.pushes, "a")[0].env
	var payload protocol.ContextUsagePayload
	if err := json.Unmarshal(pushA.Payload, &payload); err != nil {
		t.Fatalf("decode context_usage: %v", err)
	}
	if payload.ConversationID != testConvID {
		t.Errorf("conversation_id = %q, want %q", payload.ConversationID, testConvID)
	}
	if payload.DroppedCategories != 3 || payload.DroppedMCPTools != 5 || payload.DroppedMemoryFiles != 7 {
		t.Errorf("dropped counts = (%d,%d,%d), want (3,5,7) — mutually distinct, so an equal pair "+
			"here means two counts are cross-wired", payload.DroppedCategories,
			payload.DroppedMCPTools, payload.DroppedMemoryFiles)
	}
	if len(payload.Categories) != 2 || payload.Categories[0].Name != "QQ-Zulu-Category-ZZ" ||
		payload.Categories[1].Name != "QQ-Alpha-Category-ZZ" {
		t.Fatalf("category order changed: %+v", payload.Categories)
	}

	// ONE logical event: the ring records once, before the per-conn fan-out, and
	// every conn's envelope carries that one durable id.
	ring, gap := e.ring.After(testConvID, 0)
	if gap || len(ring) != 1 {
		t.Fatalf("ring = %#v (gap=%v), want one logical event", ring, gap)
	}
	if ring[0].Type != protocol.TypeContextUsage || !bytes.Equal(ring[0].Payload, pushA.Payload) {
		t.Fatalf("ring event differs from wire: ring=%s/%s wire=%s/%s",
			ring[0].Type, ring[0].Payload, pushA.Type, pushA.Payload)
	}
	if pushA.EventID == nil || *pushA.EventID != ring[0].ID {
		t.Fatalf("wire event id = %v, want ring id %d", pushA.EventID, ring[0].ID)
	}
}

// TestInteractiveTurnEmitterV2_ContextUsagePreservesMidTurnSequence pins the
// flushDelta-first ordering. The arrival order in production is post-turn, but the
// event is not structurally barred from landing mid-turn (a follow-active switch can
// reorder the lane), and buffered text must keep its wire position either way.
func TestInteractiveTurnEmitterV2_ContextUsagePreservesMidTurnSequence(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m1", Text: "before"})
	turnID, state := e.turnID, e.currentState
	e.Handle(context.Background(), emitterContextUsageFixture)
	if !e.inTurn || e.turnID != turnID || e.currentState != state {
		t.Fatalf("context_usage disturbed an OPEN turn: inTurn=%v turnID=%q/%q state=%q/%q",
			e.inTurn, e.turnID, turnID, e.currentState, state)
	}
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "after"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta, // "before", flushed AHEAD of the reading
		protocol.TypeContextUsage,
		protocol.TypeAssistantDelta,
		protocol.TypeTurnEnd,
		protocol.TypeTurnState,
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn context_usage order = %v, want %v", got, wantTypes)
	}
	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 || deltas[0].TurnID != turnID || deltas[1].TurnID != turnID ||
		deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("deltas around context_usage = %+v, want same turn %q at seq 0,1", deltas, turnID)
	}
}

// TestInteractiveTurnEmitterV2_ContextUsageLogsNameOnlyTheVariant is AC 5, swept
// across every log site the frame can reach.
//
// The temptation here is larger than for any neighbouring variant: the event carries
// a model, category names, MCP tool and server names, and memory-file PATHS — the
// operator's and the workspace's own text, exactly what a diagnostic line explaining
// an unexpected reading would reach for. None of it is returned, and neither is any
// list length or dropped count.
func TestInteractiveTurnEmitterV2_ContextUsageLogsNameOnlyTheVariant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		drive func(t *testing.T, logs *bytes.Buffer)
		want  string
	}{
		{
			// The no-cursor drop: it returns BEFORE the type switch, so it stays a
			// reachable site for this variant even once Handle claims it.
			name: "no-cursor drop",
			drive: func(t *testing.T, logs *bytes.Buffer) {
				logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				e := newInteractiveTurnEmitterV2(&stubCursor{}, &fakeInteractiveBcast{}, logger)
				e.Handle(context.Background(), emitterContextUsageFixture)
			},
			want: "kind=context_usage",
		},
		{
			// The push-failure path, which logs a TRANSPORT error. This is the site
			// whose safety would otherwise be inherited rather than checked: emit
			// logs "err", err there, and this proves that error carries no payload.
			name: "push failure",
			drive: func(t *testing.T, logs *bytes.Buffer) {
				logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				cur := &stubCursor{}
				cur.set(testConvID)
				bcast := &fakeInteractiveBcast{
					snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}},
					pushErr:   map[string]error{"a": errors.New("qq-transport-failure-zz")},
				}
				e := newInteractiveTurnEmitterV2(cur, bcast, logger)
				e.Handle(context.Background(), emitterContextUsageFixture)
			},
			want: "interactive_turn.push_err",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			tt.drive(t, &logs)

			got := logs.String()
			if !strings.Contains(got, tt.want) {
				t.Fatalf("log does not record %s: %s", tt.want, got)
			}
			if strings.Contains(got, "kind=unknown") {
				t.Errorf("log reads kind=unknown for a variant the daemon recognizes: %s", got)
			}
			for _, needle := range contextUsageNeedles() {
				if strings.Contains(got, needle) {
					t.Errorf("event content %q leaked into the log: %s", needle, got)
				}
			}
		})
	}
}

// TestInteractiveTurnEmitterV2_ContextUsageRecordsToRegistry is AC-2: the arm that
// publishes the post-turn reading also files its summary in the conversation's
// registry row, so a client that reconnects after a daemon restart is shown the
// real numbers. The wire frames are asserted alongside, because a write added to
// this arm must not disturb what it emits or its lifecycle neutrality.
func TestInteractiveTurnEmitterV2_ContextUsageRecordsToRegistry(t *testing.T) {
	t.Parallel()
	const convID = conversations.ConversationID("11111111-2222-4333-8444-555555555555")

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:         convID,
		Cwd:        "/home/user/project",
		LastUsedAt: time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC),
	})
	path := filepath.Join(t.TempDir(), "conversations.json")
	when := time.Date(2026, 9, 16, 8, 30, 15, 0, time.UTC)

	cur := &stubCursor{}
	cur.set(string(convID))
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	e.usageRec = &contextUsageRecorder{
		reg:    reg,
		path:   path,
		logger: discardLogger(),
		now:    func() time.Time { return when },
	}

	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	inTurn, turnID := e.inTurn, e.turnID
	e.Handle(context.Background(), emitterContextUsageFixture)

	if e.inTurn != inTurn || e.turnID != turnID {
		t.Errorf("the registry write disturbed turn lifecycle: inTurn=%v/%v turnID=%q/%q",
			e.inTurn, inTurn, e.turnID, turnID)
	}
	if got := pushTypes(bcast.pushes); !slices.Contains(got, protocol.TypeContextUsage) {
		t.Errorf("the arm stopped publishing: %v", got)
	}

	back, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row, ok := back.Get(convID)
	if !ok {
		t.Fatal("row missing after reload")
	}
	if row.LastContextUsage == nil {
		t.Fatal("the post-turn arm recorded no reading")
	}
	want := conversations.ContextUsageReading{
		Model:       emitterContextUsageFixture.Model,
		TotalTokens: emitterContextUsageFixture.TotalTokens,
		MaxTokens:   emitterContextUsageFixture.MaxTokens,
		Percentage:  emitterContextUsageFixture.Percentage,
		AsOf:        when,
	}
	if *row.LastContextUsage != want {
		t.Errorf("stored reading = %+v, want %+v", *row.LastContextUsage, want)
	}

	// AC-4's disk side on this producer too: the inventories never reach the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, needle := range contextUsageNeedles() {
		if needle == emitterContextUsageFixture.Model {
			continue // the model IS stored, by AC-1
		}
		if strings.Contains(string(data), needle) {
			t.Errorf("registry file carries %q from the reading's inventories", needle)
		}
	}
}
