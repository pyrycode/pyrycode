package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

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
	// This list is enumerated BY HAND, so widening one of the three variants does
	// not widen the sweep — a new claude-derived field stays untested until its
	// marker lands here. #2245's two are the case in point: Summary is unbounded
	// model prose, the field on this family with the most to leak.
	secrets := []string{
		"SECRETTASKIDZZZ", "SECRETTOOLCALLZZZ", "SECRETDESCZZZ", "SECRETTASKTYPEZZZ",
		"SECRETPATCHZZZ", "SECRETSTATUSZZZ", "SECRETSUMMARYZZZ",
		"SECRETROWIDZZZ", "SECRETROWTYPEZZZ", "SECRETROWDESCZZZ",
	}
	for _, ev := range []turnevent.Event{
		turnevent.BackgroundTaskStarted{
			TaskID:      "SECRETTASKIDZZZ",
			ToolCallID:  "SECRETTOOLCALLZZZ",
			Description: "SECRETDESCZZZ",
			TaskType:    "SECRETTASKTYPEZZZ",
		},
		// Two events of this variant, because its two producing subtypes fill
		// disjoint fields: one event can never carry both a patch and a terminal
		// state, so a single fixture would leave one half of the variant unswept.
		turnevent.BackgroundTaskUpdated{TaskID: "SECRETTASKIDZZZ", Patch: "SECRETPATCHZZZ"},
		turnevent.BackgroundTaskUpdated{
			TaskID: "SECRETTASKIDZZZ", Status: "SECRETSTATUSZZZ", Summary: "SECRETSUMMARYZZZ",
		},
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

// AC1's wire leg: a turnevent.BackgroundTaskProgress reaches a mobile client as a
// background_task_progress frame carrying conversation identity and every one of the
// event's fields — and only a phone that negotiated `interactive` receives it.
//
// Without this the whole mapping stops at the daemon boundary: MapEvent would return
// ok == false, the emitter's default arm would debug-log it as an unknown event, and
// nothing would reach a phone. That is not hypothetical — it is the state #1385 left
// behind and #1386 had to open a second ticket to fix, which is why this frame's
// producer and its emitter arm ship together.
//
// Every fixture value differs so a handler that crossed two fields goes red here.
func TestInteractiveTurnEmitterV2_BackgroundTaskProgressFansOutToInteractiveOnly(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "a", Interactive: true},
		{ConnID: "b", Interactive: false},
	}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	e.Handle(context.Background(), turnevent.BackgroundTaskProgress{
		TaskID:       "a8eec1cd5e109aa38",
		Description:  "Reading beta.txt",
		SubagentType: "general-purpose",
		LastToolName: "Read",
		TotalTokens:  16246,
		ToolUses:     2,
		DurationMS:   4546,
	})

	if got := len(bcast.pushes); got != 1 {
		t.Fatalf("pushed %d envelopes; want exactly 1 (interactive conn only)", got)
	}
	p := bcast.pushes[0]
	if p.connID != "a" {
		t.Fatalf("pushed to conn %q; want interactive conn %q", p.connID, "a")
	}
	if p.env.Type != protocol.TypeBackgroundTaskProgress {
		t.Fatalf("envelope type: got %q, want %q", p.env.Type, protocol.TypeBackgroundTaskProgress)
	}
	if len(pushesFor(bcast.pushes, "b")) != 0 {
		t.Fatalf("non-interactive conn b received the %s frame", protocol.TypeBackgroundTaskProgress)
	}

	var got protocol.BackgroundTaskProgressPayload
	if err := json.Unmarshal(p.env.Payload, &got); err != nil {
		t.Fatalf("decode background_task_progress payload: %v", err)
	}
	want := protocol.BackgroundTaskProgressPayload{
		ConversationID: testConvID,
		TaskID:         "a8eec1cd5e109aa38",
		Description:    "Reading beta.txt",
		SubagentType:   "general-purpose",
		LastToolName:   "Read",
		TotalTokens:    16246,
		ToolUses:       2,
		DurationMS:     4546,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload:\n got %#v\nwant %#v", got, want)
	}
	// The wire literal is the daemon's own name, never claude's system/task_progress
	// subtype and never its unrelated top-level tool_progress type. Asserted on the
	// envelope BYTES reaching the conn, which a constant splice cannot launder;
	// internal/protocol's TestBackgroundTaskProgressType_IsNotClaudesSubtype pins the
	// constant itself.
	if p.env.Type != "background_task_progress" {
		t.Fatalf("wire type literal: got %q, want %q", p.env.Type, "background_task_progress")
	}
}

// AC1's lifecycle leg: a background-task progress frame opens and closes no turn.
//
// It matters more for this variant than for its three background-task neighbours,
// because this one fires REPEATEDLY while a task runs: a handler that opened a turn
// on it would wedge the conversation once per progress line, on work that is
// orthogonal to the turn by construction. The frame is handled bare before any turn,
// the tracker's fields are asserted directly, and a following content event proves a
// fresh turn still opens afterwards.
func TestInteractiveTurnEmitterV2_BackgroundTaskProgressNoLifecycleMutation(t *testing.T) {
	t.Parallel()
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// Two of them, because one frame cannot show that a repeating variant leaves the
	// tracker alone every time rather than only the first time.
	for _, uses := range []int{2, 4} {
		e.Handle(context.Background(), turnevent.BackgroundTaskProgress{TaskID: "t-1", ToolUses: uses})
	}

	// A turn_state anywhere in the sequence is the observable signature of a
	// transitionTo call, so the exact frame sequence is the assertion.
	want := []string{protocol.TypeBackgroundTaskProgress, protocol.TypeBackgroundTaskProgress}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, want) {
		t.Fatalf("bare background_task_progress envelopes: got %v, want %v", got, want)
	}
	if e.inTurn {
		t.Error("background_task_progress opened a turn; inTurn must stay false")
	}
	if e.turnID != "" {
		t.Errorf("background_task_progress minted a turn id: got %q, want empty", e.turnID)
	}
	if e.currentState != "" {
		t.Errorf("background_task_progress set currentState: got %q, want empty", e.currentState)
	}

	e.Handle(context.Background(), turnevent.TextChunk{Text: "hello"})
	e.flushDelta(context.Background())
	wantTypes := []string{
		protocol.TypeBackgroundTaskProgress,
		protocol.TypeBackgroundTaskProgress,
		protocol.TypeTurnState,      // responding — a fresh turn opens afterwards
		protocol.TypeAssistantDelta, // hello
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("post-progress envelopes:\n got %v\nwant %v", got, wantTypes)
	}
}
