package main

import (
	"context"
	"errors"
	goparser "go/parser"
	"go/token"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// stubBusyResolve builds a session→conversation resolver over a fixed map — the
// unit-tier stand-in for conversationForSession(w.convReg, sid). A session absent
// from the map resolves ok=false, which is the unbound-bootstrap case.
func stubBusyResolve(m map[string]string) func(string) (string, bool) {
	return func(sid string) (string, bool) {
		conv, ok := m[sid]
		return conv, ok
	}
}

// requireWaitIdle asserts WaitIdle returns nil within a bounded deadline. The
// call runs in a goroutine rather than inline so a regression to "blocks forever
// on an idle conversation" fails this test instead of hanging the whole suite.
func requireWaitIdle(t *testing.T, tr *turnBusyTracker, convID string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- tr.WaitIdle(context.Background(), convID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitIdle(%q) = %v, want nil", convID, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("WaitIdle(%q) never returned for an idle conversation", convID)
	}
}

// --- unit tier: the tracker driven directly ---------------------------------

// AC4: the opener set is a WHITELIST — exactly the four variants that open a turn
// today, with everything else leaving the conversation idle.
//
// This unit tier is the ONLY tier where the whitelist is observable. Stall,
// ApiRetry and Compacting are tui-driver signals (turnevent/event.go:73-96) and
// the stream-json sink has exactly one producer — streamsup.Parser, which emits
// five variants only (parser.go:157-220). None of the three can reach the tracker
// through a parser or a live runner, so feeding them here by hand is not a
// simulation of a reachable input: it pins the type switch against the parser's
// documented growth path (its default: arm tolerates rate_limit_event today,
// parser.go:158-163, which is precisely the line that becomes an ApiRetry the day
// someone wires it). A blacklist ("anything that isn't TurnEnd opens a turn") is
// behaviourally identical through today's sink and would wedge a conversation on
// that first new variant.
func TestTurnBusyTracker_OpenerWhitelist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ev       turnevent.Event
		wantBusy bool
	}{
		{"thought_chunk opens", turnevent.ThoughtChunk{MessageID: "m1", Text: "thinking"}, true},
		{"text_chunk opens", turnevent.TextChunk{MessageID: "m1", Text: "hello"}, true},
		{"tool_start opens", turnevent.ToolStart{ToolCallID: "tu-1", Title: "Read"}, true},
		{"tool_update opens", turnevent.ToolUpdate{ToolCallID: "tu-1"}, true},
		{"stall does not open", turnevent.Stall{}, false},
		{"api_retry does not open", turnevent.ApiRetry{Active: true, Current: 1, Total: 3}, false},
		{"compacting does not open", turnevent.Compacting{Active: true}, false},
		{"lone turn_end does not open", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
			tr.observe("sess-a", tc.ev)
			if got := tr.Busy(testConvID); got != tc.wantBusy {
				t.Errorf("Busy after %T = %v, want %v", tc.ev, got, tc.wantBusy)
			}
		})
	}
}

// AC4: an open turn clears on TurnEnd for either stop reason. Both reasons
// traverse one arm in the producer (resultTurnEndReason, parser.go:173, only
// picks the reason field), so this asserts one code path twice — the AC names
// both reasons, so both are asserted.
func TestTurnBusyTracker_ClearsOnBothStopReasons(t *testing.T) {
	t.Parallel()

	for _, reason := range []turnevent.TurnEndReason{
		turnevent.TurnEndReasonEndTurn,
		turnevent.TurnEndReasonCancelled,
	} {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())

			tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})
			if !tr.Busy(testConvID) {
				t.Fatalf("Busy = false after an opener, want true")
			}
			tr.observe("sess-a", turnevent.TurnEnd{Reason: reason})
			if tr.Busy(testConvID) {
				t.Errorf("Busy = true after TurnEnd{%s}, want false", reason)
			}
		})
	}
}

// AC1 (existence-oracle discipline): unknown, never-seen and empty conversation
// ids all report idle, and WaitIdle returns promptly for each. A foreign id
// traverses the identical map lookup as an idle one, so the two are
// indistinguishable in value and in code path — the #1101 posture
// screenSnapshotterOrNil records (relay.go:395-410).
func TestTurnBusyTracker_UnknownConversationReportsIdle(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
	for _, convID := range []string{
		"",                                     // empty
		"99999999-9999-4999-8999-999999999999", // foreign / unknown
		testConvID,                             // known to the resolver, never seen by the tracker
	} {
		if tr.Busy(convID) {
			t.Errorf("Busy(%q) = true on a tracker that has observed nothing, want false", convID)
		}
		requireWaitIdle(t, tr, convID)
	}
}

// AC1: a producing session that resolves to no conversation is NOT tracked —
// never under an empty key, which would both wedge that key and collide with the
// unknown-conversation answer above. Both halves of the guard are pinned: a
// resolver that fails, and one that succeeds with an empty id.
func TestTurnBusyTracker_UnresolvableSessionNotTracked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		resolve func(string) (string, bool)
	}{
		{"resolve reports not found", func(string) (string, bool) { return "", false }},
		{"resolve reports an empty id", func(string) (string, bool) { return "", true }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := newTurnBusyTracker(tc.resolve, discardLogger())
			tr.observe("sess-x", turnevent.TextChunk{MessageID: "m1", Text: "hello"})

			if tr.Busy("") {
				t.Error(`Busy("") = true; the event was tracked under an empty key`)
			}
			if tr.Busy(testConvID) {
				t.Errorf("Busy(%q) = true; an unresolvable session must track nothing", testConvID)
			}
		})
	}
}

// AC3 (first half): tracking is per-conversation. A turn open on A leaves B idle,
// and closing A leaves both idle. No cursor exists anywhere in this test — that
// absence is the point: the tracker holds no cursor reference at all, so a cursor
// move cannot change any verdict. (The second half of AC3 — a turn on a
// NON-ACTIVE conversation still reporting busy — needs the real drain and lives
// in TestStreamTurnDrainV2_BusyFedBeforeActiveGate.)
func TestTurnBusyTracker_PerConversationIndependence(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
		"sess-b": testConvIDB,
	}), discardLogger())

	tr.observe("sess-a", turnevent.TextChunk{MessageID: "ma", Text: "hello"})
	if !tr.Busy(testConvID) {
		t.Errorf("Busy(A) = false with a turn open on A, want true")
	}
	if tr.Busy(testConvIDB) {
		t.Errorf("Busy(B) = true with no turn on B, want false")
	}

	tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	if tr.Busy(testConvID) || tr.Busy(testConvIDB) {
		t.Errorf("after A's TurnEnd: Busy(A) = %v, Busy(B) = %v, want both false",
			tr.Busy(testConvID), tr.Busy(testConvIDB))
	}
}

// AC2: WaitIdle blocks while the conversation has an open turn and returns nil
// once its TurnEnd lands.
func TestTurnBusyTracker_WaitIdleBlocksUntilTurnEnd(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})

	done := make(chan error, 1)
	go func() { done <- tr.WaitIdle(context.Background(), testConvID) }()

	// The wait must still be outstanding while the turn is open. The grace window
	// can never fail a correct WaitIdle (it never returns while busy); it only
	// fires when WaitIdle returned early.
	select {
	case err := <-done:
		t.Fatalf("WaitIdle returned (%v) while a turn was open", err)
	case <-time.After(50 * time.Millisecond):
	}

	tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitIdle = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitIdle never returned after the conversation's TurnEnd")
	}
}

// AC2: the blocking form returns on context cancellation rather than waiting for
// a TurnEnd that may never come.
func TestTurnBusyTracker_WaitIdleHonoursCancel(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.WaitIdle(ctx, testConvID) }()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("WaitIdle after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitIdle never returned after its context was cancelled")
	}

	// The turn is still open — cancellation reports the caller gave up waiting, it
	// does not clear the conversation.
	if !tr.Busy(testConvID) {
		t.Error("Busy = false after a cancelled wait; cancellation must not clear the turn")
	}
}

// --- #1202: the teardown feed ------------------------------------------------

// #1202 AC1/AC2: clearForSession closes an open turn when the conversation's
// session is torn down. NO turn event of any kind is fed after the opener in any
// sub-case, and that absence is the point: the failure mode this feed exists to
// close is precisely the one where the stream has gone SILENT (the abandoned
// turn's result line never comes), so a TurnEnd anywhere here would be exercising
// the OTHER feed and would stay green with clearForSession deleted.
//
// The "unrelated" and "unresolvable" sub-cases are the spurious-clear direction,
// which is the costly one. A missed clear wedges a conversation busy (fails
// closed); a clear on the wrong conversation reports a LIVE turn as idle and,
// under the consuming slice, releases a mid-turn send into it.
func TestTurnBusyTracker_ClearForSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		openFor   string // session id fed one opener; "" opens nothing
		clear     string // session id handed to clearForSession
		wantBusyA bool
		wantBusyB bool
	}{
		{"teardown of the owning session closes the turn", "sess-a", "sess-a", false, false},
		{"teardown of an unrelated session leaves the turn open", "sess-a", "sess-b", true, false},
		{"teardown of an unresolvable session clears nothing", "sess-a", "sess-gone", true, false},
		{"teardown of an already-idle conversation is a no-op", "", "sess-a", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
				"sess-a": testConvID,
				"sess-b": testConvIDB,
			}), discardLogger())

			if tc.openFor != "" {
				tr.observe(tc.openFor, turnevent.TextChunk{MessageID: "m1", Text: "hello"})
			}
			tr.clearForSession(tc.clear)

			if got := tr.Busy(testConvID); got != tc.wantBusyA {
				t.Errorf("Busy(A) after clearForSession(%q) = %v, want %v", tc.clear, got, tc.wantBusyA)
			}
			if got := tr.Busy(testConvIDB); got != tc.wantBusyB {
				t.Errorf("Busy(B) after clearForSession(%q) = %v, want %v", tc.clear, got, tc.wantBusyB)
			}
		})
	}
}

// #1202 AC4 (tracker tier): a nil receiver is a no-op. The pool's transition
// observer is wired UNCONDITIONALLY (relay.go) while the tracker is constructed
// only on the stream path, so in the daemon's default PTY mode this method IS
// called on a nil tracker at the first /clear or eviction — on the pool's own
// lifecycle goroutine. Busy and WaitIdle deliberately do NOT carry this guard
// (they take t.mu immediately), which is why it has to live on the method the
// unconditional wiring actually calls.
func TestTurnBusyTracker_ClearForSessionNilReceiver(t *testing.T) {
	t.Parallel()

	var tr *turnBusyTracker
	tr.clearForSession("sess-a") // must not panic
}

// #1202 AC1: a blocked WaitIdle is woken by the teardown clear, not only by a
// TurnEnd. This is the test that fails if clearForSession mutates the map without
// the close-and-replace broadcast: Busy would already read idle while every
// waiter stayed parked on a generation channel that is never closed.
func TestTurnBusyTracker_ClearForSessionWakesWaitIdle(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})

	done := make(chan error, 1)
	go func() { done <- tr.WaitIdle(context.Background(), testConvID) }()

	// The wait must still be outstanding while the turn is open. The grace window
	// can never fail a correct WaitIdle; it only fires when WaitIdle returned early.
	select {
	case err := <-done:
		t.Fatalf("WaitIdle returned (%v) while a turn was open", err)
	case <-time.After(50 * time.Millisecond):
	}

	tr.clearForSession("sess-a")
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitIdle = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitIdle never returned after the conversation's session was torn down")
	}
}

// AC2 (-race): concurrent readers on arbitrary goroutines against a single
// goroutine driving transitions. The assertion is race cleanliness plus
// termination — every reader exits and both conversations end idle — not any
// intermediate value, which is genuinely unobservable here.
func TestTurnBusyTracker_ConcurrentReadersAndDriver(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
		"sess-b": testConvIDB,
	}), discardLogger())

	const cycles = 200
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for range cycles {
			tr.observe("sess-a", turnevent.TextChunk{MessageID: "ma", Text: "a"})
			tr.observe("sess-b", turnevent.ToolStart{ToolCallID: "tu-b", Title: "Read"})
			tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
			tr.observe("sess-b", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled})
		}
	}()

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = tr.Busy(testConvID)
				_ = tr.Busy(testConvIDB)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
				_ = tr.WaitIdle(ctx, testConvID)
				cancel()
			}
		}()
	}
	wg.Wait()

	if tr.Busy(testConvID) || tr.Busy(testConvIDB) {
		t.Errorf("after balanced open/close cycles: Busy(A) = %v, Busy(B) = %v, want both false",
			tr.Busy(testConvID), tr.Busy(testConvIDB))
	}
}

// AC1, structurally: the tracker derives its signal from the fan-in events alone.
// The import block is the machine-checkable form of "no second parse of claude's
// stdout, no inference from stdin writes, no timer" — a time / os / io / transcript
// import would be the first symptom of any of those, and this test fails on it
// before a reviewer has to notice.
func TestTurnBusyTracker_ImportsStayMinimal(t *testing.T) {
	t.Parallel()

	f, err := goparser.ParseFile(token.NewFileSet(), "stream_turn_busy.go", nil, goparser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse stream_turn_busy.go: %v", err)
	}
	got := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		got = append(got, strings.Trim(imp.Path.Value, `"`))
	}
	slices.Sort(got)

	want := []string{
		"context",
		"github.com/pyrycode/pyrycode/internal/turnevent",
		"log/slog",
		"sync",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stream_turn_busy.go imports:\n got %v\nwant %v", got, want)
	}
}

// --- drain tier: the real drain and the real streamsup.Parser ---------------

// AC3 (second half — the teeth): the tracker is fed BEFORE the drain's
// active-session gate, so a turn running on a NON-ACTIVE conversation is still
// reported busy.
//
// The cursor and the gate both point at conversation B; session A's bytes are fed
// and dropped at the gate. Two designs fail this test and only this test: a
// tracker fed AFTER the gate never hears about A (it would report A idle because
// it never heard, not because A is idle), and a tracker deriving its key from the
// active-conversation cursor would attribute A's turn to B.
func TestStreamTurnDrainV2_BusyFedBeforeActiveGate(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvIDB) // cursor on conversation B
	active := &stubActiveSession{}
	active.set("sess-b") // ...and B's bound session is what the gate admits
	bcast := newChanBcast("conn-b")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	busy := newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
		"sess-b": testConvIDB,
	}), discardLogger())

	drops := make(chan string, 8)
	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, busy,
		slog.New(dropWatcher{kinds: drops}))
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	feedLines(sink, "sess-a", assistantTextLine("ma", "for-A"))

	// Barrier: the not-active drop is logged AFTER observe on the same goroutine,
	// so seeing the drop proves the tracker was already fed. No sleep.
	waitDropKind(t, drops, "text_chunk")

	if !busy.Busy(testConvID) {
		t.Errorf("Busy(A) = false for a turn running on the non-active conversation A, want true")
	}
	if busy.Busy(testConvIDB) {
		t.Errorf("Busy(B) = true; B has no open turn, only the cursor")
	}
	assertNoPush(t, bcast.pushed)
}

// The full open→close cycle through the real parser: the four variants that reach
// the sink are produced by streamsup.Parser alone, so this is the reachable-input
// counterpart to the unit-tier whitelist table.
func TestStreamTurnDrainV2_BusyOpenThenCloseThroughParser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set("sess-a")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	busy := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, busy, discardLogger())
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	feedLines(sink, "sess-a", assistantTextLine("m1", "hello"))

	// Barrier: the responding turn_state is pushed from inside Handle, which the
	// drain calls strictly after observe.
	if got := collectEnvs(t, bcast.pushed, 1); got[0].Type != protocol.TypeTurnState {
		t.Fatalf("first envelope = %s, want %s", got[0].Type, protocol.TypeTurnState)
	}
	if !busy.Busy(testConvID) {
		t.Errorf("Busy = false after an assistant text line opened the turn, want true")
	}

	feedLines(sink, "sess-a", resultLine)

	// Barrier: assistant_delta, turn_end, turn_state(idle) — that order is
	// invariant even if the ~250ms coalescing timer flushes the delta early.
	collectEnvs(t, bcast.pushed, 3)
	if busy.Busy(testConvID) {
		t.Errorf("Busy = true after the result line's TurnEnd, want false")
	}
}
