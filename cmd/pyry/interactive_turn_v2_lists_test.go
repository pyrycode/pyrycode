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
// too — stream_turn_busy.go and stream_turn_drain.go — which would otherwise
// read kind=unknown for a variant the daemon does recognize.
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
			Name:         "qq-name-alpha-sentinel",
			ArgumentHint: "qq-hint-alpha-sentinel",
			Description:  "qq-description-alpha-sentinel",
			// TWO aliases where the other entry has one, so this field is
			// distinguishable in cardinality as well as in bytes — and both are
			// NON-EMPTY, which the sentinel derivation's own doc requires of every
			// needle it feeds to strings.Contains.
			Aliases:         []string{"qq-alias-alpha-sentinel", "qq-alias-alpha-second-sentinel"},
			TruncatedFields: []string{"qq-truncated-alpha-sentinel"},
		},
		{
			Name:         "zz-name-beta-sentinel",
			ArgumentHint: "zz-hint-beta-sentinel",
			Description:  "zz-description-beta-sentinel",
			// Non-empty here too, and deliberately NOT nil even though nil is this
			// field's own load-bearing absence: an entry with no aliases contributes
			// no needle at all, so the field's leak would go unswept on this entry
			// while every assertion stayed green. TruncatedFields carries the nil
			// case for this fixture and is the right field for it, being the one the
			// family's absence convention is about.
			Aliases:         []string{"zz-alias-beta-sentinel"},
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
// Description is the first one to pay it, ArgumentHint (#1957) the second, Aliases
// (#1825) the third and last, and the first of them is the proof the claim needed
// correcting.
//
// THE THIRD ONE IS A LIST AND PAYS THE SAME DEBT ELEMENT BY ELEMENT, appended with
// `...` exactly as TruncatedFields is. What it adds is a second way to under-sweep
// that no scalar field has an analogue for: an entry whose alias list is EMPTY
// contributes NO needle rather than an empty one, so it would be silently exempt
// from the sweep instead of failing it. That is why every entry of the fixture
// carries at least one alias — the same requirement as the non-empty paragraph
// below, for a different underlying reason: there the danger is a vacuously-true
// needle, here an absent one.
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
		out = append(out, c.Aliases...)
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

// assertSlashCommandListKindLeaksNothing is the drop-site half of AC#3: the
// captured log names the variant, does not read as unknown, and carries nothing
// derived from the event — no entry's Name, no entry's TruncatedFields, and not the
// entry count.
//
// It had two callers until #2003 gave Handle a case for the variant, which retired
// the live-cursor one; the surviving caller is the empty-cursor test below. Kept a
// helper rather than inlined, because the drop sites it is shaped for outlive that
// test — stream_turn_drain.go's sink-full and not-active-session drops route
// through the same eventKind — and because this is where the ENTRY-COUNT needle
// can live at all: its logger drops slog's time attr and its records carry no
// env_id, so unlike the emit-path rig in TestInteractiveTurnEmitterV2_NoAppOutputLogLeak
// there is no digit source for a small count to collide with.
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
// model_list test above, and since #2003 it is the ONLY thing keeping this
// assertion reachable on this lane: with a live cursor the Handle case claims the
// event and it reaches no eventKind call site at all. Handle's default arm is no
// longer among the live sites — that is what the live-cursor test below became when
// it was replaced by its positive twin. The ones that remain are the no-cursor drop
// this test drives, which returns before the type switch, and stream_turn_drain.go's
// sinkFor sink-full drop and startStreamTurnDrainV2 not-active-session drop, both of
// which would otherwise read kind=unknown for a variant the daemon does recognize.
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

// #2003 AC#1: a slash-command list reaches every interactive conn as one
// slash_command_list envelope carrying exactly what turnbridge.MapEvent produced
// for the event, and reaches no non-interactive conn.
//
// This is #1854's tripwire REPLACED BY ITS POSITIVE TWIN rather than deleted: that
// test pinned "with a conversation routed the event reaches Handle's DEFAULT arm,
// so nothing publishes it", and its own doc named this slice as the one that would
// redden it. What it cannot keep is the drop-site log helper — with a live cursor
// and an arm that claims the event there is NO drop record at all, so a helper that
// Fatals on an empty log would be asserting the feature did not ship. The emit
// path's log negative belongs to the push-error rig in
// TestInteractiveTurnEmitterV2_NoAppOutputLogLeak, which is where AC#3 discharges
// it; the surviving drop-site assertion is the empty-cursor test above.
//
// This is the ONLY test in the family that decodes payload fields; the rest assert
// on pushTypes. The per-field want below is built from the fixture's own fields
// rather than from re-typed literals, which keeps it honest about a swapped
// assignment inside MapEvent's loop: every field of every entry holds a distinct
// sentinel, so a Name/Description swap or an entry-index swap is red.
//
// dropped_commands is deliberately NOT asserted, and its absence is the assertion
// this test would otherwise get wrong. #2002's byte cut composes with streamsup's
// entry cut, so the count on the wire is the event's own DroppedCommands PLUS
// whatever the frame bound dropped; internal/turnbridge already pins that
// composition, and pinning it here against the fixture's own count would assert
// the two cuts do not compose.
func TestInteractiveTurnEmitterV2_SlashCommandListFansOutToEveryInteractiveConn(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: true},
		{ConnID: "c", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), emitterSlashCommandListFixture)

	for _, connID := range []string{"a", "b"} {
		got := pushesFor(bcast.pushes, connID)
		if len(got) != 1 {
			t.Fatalf("conn %s received %d envelopes; want exactly 1", connID, len(got))
		}
		if got[0].env.Type != protocol.TypeSlashCommandList {
			t.Fatalf("conn %s envelope type: got %q, want %q", connID, got[0].env.Type, protocol.TypeSlashCommandList)
		}
	}
	if got := pushesFor(bcast.pushes, "c"); len(got) != 0 {
		t.Fatalf("non-interactive conn received %d envelopes; want 0", len(got))
	}

	var pl protocol.SlashCommandListPayload
	if err := json.Unmarshal(pushesFor(bcast.pushes, "a")[0].env.Payload, &pl); err != nil {
		t.Fatalf("decode slash_command_list payload: %v", err)
	}
	if pl.ConversationID != testConvID {
		t.Errorf("conversation_id: got %q, want %q", pl.ConversationID, testConvID)
	}
	src := emitterSlashCommandListFixture.Commands
	want := []protocol.SlashCommand{
		{
			Name:            src[0].Name,
			ArgumentHint:    src[0].ArgumentHint,
			Description:     src[0].Description,
			Aliases:         src[0].Aliases,
			TruncatedFields: src[0].TruncatedFields,
		},
		{
			Name:         src[1].Name,
			ArgumentHint: src[1].ArgumentHint,
			Description:  src[1].Description,
			Aliases:      src[1].Aliases,
			// nil, and it stays nil through the round trip: SlashCommand.MarshalJSON
			// exempts this field from the nil -> [] normalisation it applies to
			// Aliases one field up, so nothing-was-cut reaches the wire as null. The
			// asymmetry is the wire contract; a test that "fixed" it would assert the
			// opposite of it.
			TruncatedFields: src[1].TruncatedFields,
		},
	}
	if !reflect.DeepEqual(pl.Commands, want) {
		t.Fatalf("slash_command_list rows on the wire:\n got %+v\nwant %+v", pl.Commands, want)
	}
}

// #2003 AC#2: a slash_command_list frame opens and closes no turn. It is handled
// bare before any turn and emits exactly one frame — no turn_state, no turn_end —
// and the tracker's inTurn/turnID/currentState are asserted directly, then a
// following content event is driven through to prove a fresh turn still opens.
//
// The lifecycle answer is the one TestTurnMarkFor_TotalOverEveryVariant already
// pins for this variant as turnMarkNone; this is the emitter agreeing with it. It
// matters here for the model_list test's reason above: the list is a property of
// the CHILD, reported once per initialize exchange, so a turn opened on one has no
// turn end anywhere in its future to clear it.
func TestInteractiveTurnEmitterV2_SlashCommandListNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), emitterSlashCommandListFixture)

	// A turn_state anywhere in the sequence is the observable signature of a
	// transitionTo call, so the exact single-frame sequence is the assertion.
	if got := pushTypes(bcast.pushes); !slices.Equal(got, []string{protocol.TypeSlashCommandList}) {
		t.Fatalf("bare slash_command_list envelopes: got %v, want [%s]", got, protocol.TypeSlashCommandList)
	}
	if e.inTurn {
		t.Error("slash_command_list opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("slash_command_list minted a turn id: got %q, want empty", e.turnID)
	}
	if e.currentState != "" {
		t.Errorf("slash_command_list set currentState: got %q, want empty", e.currentState)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeSlashCommandList,
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-slash_command_list envelopes:\n got %v\nwant %v", got, wantTypes)
	}
	if got := turnStateValues(t, bcast.pushes); !slices.Equal(got, []string{"responding"}) {
		t.Fatalf("turn_state after slash_command_list: got %v, want [responding]", got)
	}
}

// #2003 AC#2: mid-turn, buffered assistant text keeps its wire position AHEAD of
// the frame, and the open turn survives the interleave untouched.
//
// The load-bearing part is the event driven PAST the frame. A frame-local check
// passes even if the handler called endTurn, because an endTurn on an already-open
// turn only shows its damage on the NEXT event, when a fresh turn gets minted. So
// the second delta's turn_id and the unbroken seq are what actually bite here.
func TestInteractiveTurnEmitterV2_SlashCommandListMidTurnDoesNotDisturbOpenTurn(t *testing.T) {
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

	e.Handle(context.Background(), emitterSlashCommandListFixture)

	if !e.inTurn {
		t.Error("slash_command_list closed the open turn; inTurn must stay true")
	}
	if e.turnID != beforeTurnID {
		t.Errorf("slash_command_list changed turnID: got %q, want %q", e.turnID, beforeTurnID)
	}
	if e.currentState != beforeState {
		t.Errorf("slash_command_list changed currentState: got %q, want %q", e.currentState, beforeState)
	}

	// Drive one event past the frame: this is what catches an endTurn.
	e.Handle(context.Background(), turnevent.TextChunk{MessageID: "m2", Text: "a2"})
	e.Handle(context.Background(), turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	wantTypes := []string{
		protocol.TypeTurnState,        // responding
		protocol.TypeAssistantDelta,   // a1, flushed AHEAD of the frame
		protocol.TypeSlashCommandList, // no surrounding turn_state
		protocol.TypeAssistantDelta,   // a2, flushed by turn_end
		protocol.TypeTurnEnd,          //
		protocol.TypeTurnState,        // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("mid-turn slash_command_list envelope order:\n got %v\nwant %v", got, wantTypes)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("want 2 assistant_delta, got %d", len(deltas))
	}
	if deltas[0].TurnID != beforeTurnID || deltas[1].TurnID != beforeTurnID {
		t.Fatalf("slash_command_list split the turn: %q, %q want both %q", deltas[0].TurnID, deltas[1].TurnID, beforeTurnID)
	}
	if deltas[0].Seq != 0 || deltas[1].Seq != 1 {
		t.Fatalf("slash_command_list disrupted seq: got %d,%d want 0,1", deltas[0].Seq, deltas[1].Seq)
	}
}
