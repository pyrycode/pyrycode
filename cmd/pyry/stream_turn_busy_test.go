package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
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
// Stall, ApiRetry and Compacting are tui-driver signals that the stream-json
// sink's only producer —
// streamsup.Parser — never emits, so feeding them here by hand pins the type
// switch rather than simulating a reachable input.
//
// Unrecognized is the case that proves the whitelist was worth having. It IS
// reachable: the parser emits it for any claude output outside the measured
// known-ignored list, and it reached this tracker without one line of change
// here, because the opener set is a whitelist and a new variant falls to the
// default. A blacklist ("anything that isn't TurnEnd opens a turn") is
// behaviourally identical through the older sink, and would have wedged every
// conversation that met an unknown message — the turn would open and no turn end
// would ever follow, because we could not understand the message that opened it.
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
		{"unrecognized does not open", turnevent.Unrecognized{
			Site: turnevent.UnrecognizedLineType,
			Kind: "some_future_event",
			Raw:  `{"type":"some_future_event"}`,
		}, false},
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
// traverse one arm in the producer (resultTurnEndReason, `maxTaskRosterDescription`, only
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
// screenSnapshotterOrNil records.
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
// stdout, no inference from stdin writes, no timer" — an os / io / transcript
// import would be the first symptom of any of those, and this test fails on it
// before a reviewer has to notice.
//
// "time" is admitted for #1199's waitIdleForDelivery, whose bound arrives as a
// time.Duration parameter — so the import list alone no longer carries the
// no-timer half of the guard, and the second assertion below carries it instead:
// every time.X reference in the file must be time.Duration. A clock READ
// (time.Now / After / Tick / NewTimer / Sleep) is what "no timer" always meant —
// membership must move only on an event, never on the passage of time — and that
// is now asserted directly rather than inferred from the import set. Deriving the
// bound from a duration the CALLER supplies is not a clock read: it becomes a ctx
// deadline, and a deadline expiring reports that this waiter gave up, never that
// the conversation went idle.
func TestTurnBusyTracker_ImportsStayMinimal(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "stream_turn_busy.go", nil, 0)
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
		"time",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stream_turn_busy.go imports:\n got %v\nwant %v", got, want)
	}

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "time" {
			return true
		}
		if sel.Sel.Name != "Duration" {
			t.Errorf("%s: stream_turn_busy.go references time.%s; the tracker must read no clock — "+
				"membership moves on an event, never on the passage of time",
				fset.Position(sel.Pos()), sel.Sel.Name)
		}
		return true
	})
}

// --- #1496: the shared classifier ---------------------------------------------

// turnEventVariants is the complete set of turnevent.Event variant names, read
// from the SEALING MECHANISM itself: the unexported isTurnEvent marker method is
// what closes the sum type, so every declaration of it is a variant and nothing
// else can be one. Reading the markers rather than a hand-kept list is what makes
// the totality table below fail when a variant is ADDED — a list would simply
// stay silent, which is the failure mode #1496 exists to close.
//
// The whole directory is walked rather than one file, so moving a variant to a
// new file inside the package cannot make it invisible here. _test.go files are
// skipped: a test-only variant is not part of the production set.
func turnEventVariants(t *testing.T) []string {
	t.Helper()

	const dir = "../../internal/turnevent"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := goparser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "isTurnEvent" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			// Value receivers throughout — the events are pure value types, which is
			// the property the marker block asserts.
			if id, ok := fn.Recv.List[0].Type.(*ast.Ident); ok {
				names = append(names, id.Name)
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("found no isTurnEvent markers in %s; the totality guard is asserting nothing", dir)
	}
	slices.Sort(names)
	return names
}

func turnMarkName(m turnMark) string {
	switch m {
	case turnMarkNone:
		return "turnMarkNone"
	case turnMarkOpen:
		return "turnMarkOpen"
	case turnMarkClose:
		return "turnMarkClose"
	default:
		return fmt.Sprintf("turnMark(%d)", uint8(m))
	}
}

// #1496: turnMarkFor is TOTAL over the variant set, and the table is the sole
// place the open/close/none split is stated. Two callers read it — observe, which
// applies the mark, and sinkFor, which refuses to drop a closer on a saturated
// fan-in — so a variant classified wrong here is wrong in both at once, which is
// precisely the drift the extraction forecloses.
//
// The second assertion is the totality half: the covered set must equal the
// package's isTurnEvent markers exactly, so adding a variant fails HERE rather
// than silently inheriting the default arm somewhere downstream.
//
// The recently-added variants are the rows that matter most. RateLimited pins the
// #1404 DISCHARGED note — the whitelist absorbed a genuinely new variant with no
// change to this switch — and the three background-task variants plus
// ThinkingProgress pin the same property for #1380 / #1382 / #1385: a task or a
// thinking reading is orthogonal to turn lifecycle, so neither may move the mark.
//
// ModelAnnounced (#1600) discharges the same note a SECOND time, and its row is
// the totality half doing its job rather than a policy question: the production
// switch again needed no change, and the row exists because this test — not the
// switch — is where a new variant is required to declare itself. turnMarkNone is
// correct for the whitelist's own reason: claude announcing which model it is
// running says nothing about whether a turn is open, and it arrives once per turn
// in every conversation, so opening a mark on it would wedge every one of them.
//
// SlashCommandList (#1854) is the same shape once more, and its wedge argument is
// one step stronger than ModelAnnounced's. The slash-command inventory is neither
// an opener nor a closer because it is reported once per initialize exchange,
// which is not a turn boundary and not even per-turn; the whitelist's default
// already returns turnMarkNone, so the row asserts an existing answer rather than
// a new arm. Where an announcement at least rides a turn some TurnEnd will close,
// a turn opened on an inventory has no turn end anywhere in its future to clear
// the mark.
//
// PermissionRequest is the row the marker-derived guard ADDED: it is a variant
// this fan-in cannot currently see at all, produced only on the PTY modal path
// (modalbridge's `PermissionRequestForClass`) and never by streamsup.Parser. Its
// answer is turnMarkNone, unchanged from what observe's default arm already gave
// it, and correct for the whitelist's own reason — a permission prompt is a
// question about a tool call, not a turn boundary, and the ToolStart that gated it
// already opened the turn.
func TestTurnMarkFor_TotalOverEveryVariant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ev   turnevent.Event
		want turnMark
	}{
		{turnevent.TextChunk{MessageID: "m1", Text: "hello"}, turnMarkOpen},
		{turnevent.ThoughtChunk{MessageID: "m1", Text: "thinking"}, turnMarkOpen},
		{turnevent.ToolStart{ToolCallID: "tu-1", Title: "Read"}, turnMarkOpen},
		{turnevent.ToolUpdate{ToolCallID: "tu-1"}, turnMarkOpen},
		// A heartbeat updates a row ToolStart already opened. It is neither a turn
		// boundary nor worth reserving under saturation: losing one skips a counter
		// reading, while classifying repeated readings as openers buys no lifecycle
		// transition and can crowd out a real boundary.
		{turnevent.ToolProgress{ToolCallID: "tu-1", ElapsedSeconds: 30}, turnMarkNone},
		{turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, turnMarkClose},
		{turnevent.BackgroundTaskStarted{TaskID: "t-1"}, turnMarkNone},
		{turnevent.BackgroundTaskUpdated{TaskID: "t-1"}, turnMarkNone},
		{turnevent.BackgroundTaskRoster{}, turnMarkNone},
		// #2246. Neither an opener nor a closer, and it is the strongest case in the
		// family for the whitelist's default being load-bearing rather than merely
		// correct: this variant fires REPEATEDLY while a task runs, so opening a mark
		// on one would wedge the conversation many times over on work that is
		// orthogonal to the turn. The default already answers it, so this row asserts
		// that answer rather than a new arm — turnMarkFor is unchanged by that ticket.
		{turnevent.BackgroundTaskProgress{TaskID: "t-1", ToolUses: 2}, turnMarkNone},
		{turnevent.ThinkingProgress{EstimatedTokens: 184}, turnMarkNone},
		{turnevent.RateLimited{Status: "allowed", LimitType: "five_hour"}, turnMarkNone},
		{turnevent.ModelAnnounced{Model: "claude-haiku-4-5-20251001"}, turnMarkNone},
		// #2252. Neither an opener nor a closer, and its argument is the row above's
		// taken one step further OUT rather than repeated. An announced model is a
		// property of the turn's configuration and does vary turn to turn; a build and a
		// posture are properties of the CHILD, so they say even less about whether a turn
		// is open. Both arrive on the same per-turn init line and in every conversation,
		// which is what makes the wedge argument identical: opening a mark here would
		// wedge all of them. The whitelist's default already answers it, so this row
		// asserts an existing answer rather than a new arm — turnMarkFor is unchanged by
		// that ticket.
		{turnevent.SessionFacts{ClaudeCodeVersion: "2.1.259", PermissionMode: "default"}, turnMarkNone},
		// #1811. Neither an opener nor a closer: the inventory is reported once per
		// initialize exchange, which is not a turn boundary and not even per-turn. The
		// whitelist's default already answers it, so this row asserts that answer
		// rather than a new arm — turnMarkFor is unchanged by that ticket.
		{turnevent.ModelList{Models: []turnevent.ModelOption{{ResolvedModel: "claude-sonnet-5", Value: "sonnet"}}}, turnMarkNone},
		// #1854. Neither an opener nor a closer, for the row above's reason and one
		// step more strongly: the inventory is reported once per initialize
		// exchange, which is not a turn boundary and not even per-turn. The
		// whitelist's default already answers it, so this row asserts an existing
		// answer rather than a new arm — turnMarkFor is unchanged by that ticket.
		{turnevent.SlashCommandList{Commands: []turnevent.SlashCommand{{Name: "clear"}}}, turnMarkNone},
		// #2134. Neither an opener nor a closer, and its wedge argument is the
		// SlashCommandList row's inverted rather than repeated: that inventory is not
		// even per-turn, whereas this announcement lands mid-conversation and REPLACES
		// the conversation it lands in. Opening a mark on it would wedge the OLD
		// conversation permanently — the turn end that would clear it belongs to a
		// transcript claude has already stopped writing. The whitelist's default
		// already answers it, so this row asserts an existing answer rather than a new
		// arm; turnMarkFor is unchanged by that ticket.
		//
		// turnMarkNone also makes the event DROPPABLE under sink saturation, which is
		// a real consequence rather than a side effect of the classification and is
		// #2135's to reason about at the actuator.
		{turnevent.ConversationReset{NewConversationID: "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0"}, turnMarkNone},
		{turnevent.Stall{}, turnMarkNone},
		{turnevent.ApiRetry{Active: true, Current: 1, Total: 3}, turnMarkNone},
		{turnevent.Compacting{Active: true}, turnMarkNone},
		// #2237. Neither an opener nor a closer, and its argument is the strongest on
		// this list rather than another restatement: the producer emits this for a
		// boundary line that followed no compacting edge, deliberately, so it can reach
		// the fan-in with no turn open at all. Opening a mark would then wedge the
		// conversation permanently — the turn end that would clear it may never exist.
		// The whitelist's default already answers it, so this row asserts an existing
		// answer rather than a new arm; turnMarkFor is unchanged by that ticket.
		{turnevent.CompactionBoundary{Trigger: "manual"}, turnMarkNone},
		{turnevent.Unrecognized{Site: turnevent.UnrecognizedLineType, Kind: "some_future_event"}, turnMarkNone},
		{turnevent.NewPermissionRequest("req-1", "tu-1", "Proceed?", nil), turnMarkNone},
		// #2232. Neither an opener nor a closer, and its argument is the
		// PermissionRequest row's rather than a fresh one: a denial is a verdict on a
		// tool call, not a turn boundary, and the ToolStart the parser emits for the
		// assistant tool_use before it already opened the turn. All seven captured
		// denials sit in that order — tool_use, denial, tool_result — so a turn is open
		// when this arrives and the TurnEnd that clears it still follows. The
		// whitelist's default already answers it, so this row asserts an existing
		// answer rather than a new arm; turnMarkFor is unchanged by that ticket.
		//
		// That is also why turnMarkFor's default-arm comment does NOT name this
		// variant, which is a decision rather than an oversight. It names
		// CompactionBoundary because the whitelist's answer is LOAD-BEARING there —
		// that variant can reach the fan-in with no turn open at all, so opening a mark
		// would wedge the conversation permanently. Opening one here would be merely
		// redundant, never a wedge, so "any future variant" covers it. Naming every
		// variant that lands in the default arm would grow a second enumeration of the
		// variant set beside the marker-derived one this test reads, which is the drift
		// the guard exists to foreclose.
		{turnevent.ToolCallDenied{ToolName: "Bash", ToolCallID: "toolu-1"}, turnMarkNone},
		// #2256. Neither an opener nor a closer, and its argument is CompactionBoundary's
		// rather than the row above's: both can reach the fan-in with NO TURN OPEN AT
		// ALL, where a denial always rides a turn the preceding ToolStart opened. A
		// notification belongs to claude's own queue and rides no turn, and a prompt a
		// hook refuses is never answered — so opening a mark here would wedge the
		// conversation permanently, the turn end that would clear it being one that never
		// comes. The whitelist's default already answers it, so this row asserts an
		// existing answer rather than a new arm; turnMarkFor is unchanged by that ticket.
		{turnevent.Banner{Level: "warning", Text: "blocked by hook", StopsTurn: true}, turnMarkNone},
		// #2267. Neither an opener nor a closer, and its argument is the ModelAnnounced
		// row's rather than the ToolCallDenied row's above: an announcement about WHICH
		// MODEL is running is not a boundary inside a turn. It also arrives strictly
		// INSIDE one — the fallback is a retry of a turn that ended with stop reason
		// `refusal`, so a turn is open when it lands and the TurnEnd that clears it still
		// follows. Opening a mark here would be redundant, never a wedge. The whitelist's
		// default already answers it, so this row asserts an existing answer rather than a
		// new arm; turnMarkFor is unchanged by that ticket, and its default-arm comment
		// does NOT gain this variant's name for the reason the row above states.
		{turnevent.ModelRefusalFallback{Scope: "session", FallbackModel: "claude-sonnet-4-5"}, turnMarkNone},
		// #2268. Neither an opener nor a closer. Unlike the fallback row, this line
		// announces that claude will not retry, so no later event is guaranteed; opening
		// a mark here would therefore wedge the conversation permanently. The whitelist's
		// default already answers turnMarkNone, so turnMarkFor remains unchanged.
		{turnevent.ModelRefusalNoFallback{OriginalModel: "claude-opus-4-1"}, turnMarkNone},
	}

	covered := make([]string, 0, len(tests))
	for _, tc := range tests {
		name := strings.TrimPrefix(fmt.Sprintf("%T", tc.ev), "turnevent.")
		covered = append(covered, name)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := turnMarkFor(tc.ev); got != tc.want {
				t.Errorf("turnMarkFor(%s) = %s, want %s", name, turnMarkName(got), turnMarkName(tc.want))
			}
		})
	}

	slices.Sort(covered)
	if want := turnEventVariants(t); !slices.Equal(covered, want) {
		t.Errorf("turnMarkFor table covers:\n got %v\nwant %v\n"+
			"(every turnevent.Event variant needs a row; a closer left out of the table is a conversation wedged busy forever)",
			covered, want)
	}
}

// --- #1917: in-flight tool-call retention --------------------------------------

// #1917 AC1: a tool call is reported in flight on the conversation whose stream
// carried it AND on no other, and several calls in flight at once are reported
// independently.
//
// The load-bearing test of the slice. A retention set that ignores the
// conversation key entirely — one flat set of tool-call ids with no conversation
// in it at all — satisfies every other clause of the criterion and every other
// test in this file; only the negative rows below kill it. A single-entry "most
// recent call" retention is killed by the two-calls-on-A rows, which claude's
// parallel tool_use blocks inside one assistant message make reachable.
//
// NO cursor exists anywhere in this test, copying
// TestTurnBusyTracker_PerConversationIndependence: the tracker holds no cursor
// reference at all, so a cursor move cannot change any verdict. That absence is
// the whole premise of the ticket — activeConv() moves at ENQUEUE, so a message
// enqueued for B while A's turn is parked would misattribute every later
// approval on A to B.
func TestTurnBusyTracker_ToolCallAttributedToItsOwnConversation(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
		"sess-b": testConvIDB,
	}), discardLogger())

	tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
	tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a2", Title: "Bash"})
	tr.observe("sess-b", turnevent.ToolStart{ToolCallID: "tu-b1", Title: "Read"})

	tests := []struct {
		conv string
		tool string
		want bool
	}{
		{testConvID, "tu-a1", true},
		{testConvID, "tu-a2", true}, // ...both of A's calls at once
		{testConvIDB, "tu-b1", true},
		{testConvIDB, "tu-a1", false}, // and neither of A's belongs to B
		{testConvIDB, "tu-a2", false},
		{testConvID, "tu-b1", false},
	}
	for _, tc := range tests {
		if got := tr.ToolCallInFlight(tc.conv, tc.tool); got != tc.want {
			t.Errorf("ToolCallInFlight(%q, %q) = %v, want %v", tc.conv, tc.tool, got, tc.want)
		}
	}
}

// #1917 AC1, the sharpest reading of "and as not belonging to any other": the
// SAME tool-call id in flight on two conversations at once. A collision is the
// ONLY input that separates the nested retention from a flat
// map[toolCallID]conversationID, because that flat shape is not
// conversation-blind — it stores the conversation and the report compares it —
// so it answers every distinct-id fixture in this file correctly,
// TestTurnBusyTracker_ToolCallAttributedToItsOwnConversation included.
//
// It is still the shape the spec's Security review rejected, and the collision
// is what shows why. Tool-call ids are minted by each child, so a hostile or
// confused child on B can emit a tool_use block reusing an id genuinely in
// flight on A. Flat, B's capture OVERWRITES A's entry and flips A's answer to
// false — B changing another conversation's answer, retiring the type's SECURITY
// claim that a child "can only ever mark its OWN conversation busy". Nested, B's
// fabrication lands under B's own key and the worst it achieves is a false
// positive about itself. Without this test the nesting is defended by a comment
// and nothing else: a later "simplify: one lookup, not two" refactor would leave
// the whole package green.
//
// The rows differ only in how B's claim on the shared id ends, and each asserts A
// is untouched afterwards — a flat map's sweep has to scan by value, so B's close
// takes A's entry with it too.
func TestTurnBusyTracker_CollidingToolCallIDStaysConfinedToItsConversation(t *testing.T) {
	t.Parallel()

	const shared = "tu-shared"

	tests := []struct {
		name string
		endB func(*turnBusyTracker)
	}{
		{"B's stream says the shared call finished", func(tr *turnBusyTracker) {
			tr.observe("sess-b", turnevent.ToolUpdate{ToolCallID: shared, Status: turnevent.ToolStatusCompleted})
		}},
		{"B's turn ends", func(tr *turnBusyTracker) {
			tr.observe("sess-b", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
		}},
		{"B's session is torn down", func(tr *turnBusyTracker) {
			tr.clearForSession("sess-b")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
				"sess-a": testConvID,
				"sess-b": testConvIDB,
			}), discardLogger())

			tr.observe("sess-a", turnevent.ToolStart{ToolCallID: shared, Title: "Read"})
			tr.observe("sess-b", turnevent.ToolStart{ToolCallID: shared, Title: "Read"})

			// Both, independently. A flat map cannot get past this pair: B's capture
			// has already overwritten A's entry.
			if !tr.ToolCallInFlight(testConvID, shared) {
				t.Fatalf("ToolCallInFlight(A, %q) = false while A's own stream has it in flight", shared)
			}
			if !tr.ToolCallInFlight(testConvIDB, shared) {
				t.Fatalf("ToolCallInFlight(B, %q) = false while B's own stream has it in flight", shared)
			}

			tc.endB(tr)

			if tr.ToolCallInFlight(testConvIDB, shared) {
				t.Errorf("ToolCallInFlight(B, %q) = true after B's call ended, want false", shared)
			}
			if !tr.ToolCallInFlight(testConvID, shared) {
				t.Errorf("ToolCallInFlight(A, %q) = false after B's call ended; B changed A's answer", shared)
			}
		})
	}
}

// #1917 AC2: every negative reaches false through the identical pair of map
// lookups, so the report is an existence oracle for neither id. Unknown,
// never-seen and empty values of EITHER parameter collapse to one answer — the
// posture Busy's own doc records ("the signature is the existence-oracle
// enforcement, not a runtime branch"), doubled here because the pair could
// otherwise oracle the conversation id or the tool-call id.
//
// One call is genuinely in flight on A throughout, so no row can pass merely by
// the retention being empty.
func TestTurnBusyTracker_ToolCallInFlightNegativesCollapse(t *testing.T) {
	t.Parallel()

	const foreignConv = "99999999-9999-4999-8999-999999999999"

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
		"sess-b": testConvIDB,
	}), discardLogger())
	tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})

	// The empty tool-call id is FED as a real event rather than merely never
	// inserted, which is what gives the empty rows below teeth. streamsup's
	// emitAssistant copies block.ID into ToolCallID with no non-empty check, so a
	// tool_use block that simply omits id reaches observe with an empty one; drop
	// setBusy's refusal of it and that capture lands, making
	// ToolCallInFlight(A, "") answer true — a conversation-existence oracle keyed
	// on a value the child controls by omission, which is exactly what this
	// criterion forbids. ToolCallInFlight's own doc rests on the refusal.
	tr.observe("sess-a", turnevent.ToolStart{Title: "Read"})

	if !tr.ToolCallInFlight(testConvID, "tu-a1") {
		t.Fatal("ToolCallInFlight(A, tu-a1) = false with the call in flight; every row below would pass vacuously")
	}

	tests := []struct {
		name string
		conv string
		tool string
	}{
		{"unknown conversation, known tool", foreignConv, "tu-a1"},
		{"known conversation, unknown tool", testConvID, "tu-nope"},
		{"both unknown", foreignConv, "tu-nope"},
		{"empty conversation, known tool", "", "tu-a1"},
		{"known conversation, empty tool", testConvID, ""},
		{"both empty", "", ""},
		{"a conversation the resolver knows but the tracker never saw", testConvIDB, "tu-a1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tr.ToolCallInFlight(tc.conv, tc.tool) {
				t.Errorf("ToolCallInFlight(%q, %q) = true, want false", tc.conv, tc.tool)
			}
		})
	}
}

// #1917 AC3: every close feed drops the conversation's in-flight calls. The rows
// differ only in the drop driver applied after a ToolStart on A, and a second
// call stays in flight on B throughout — so a row cannot pass by wiping
// everything, which is what an unkeyed sweep would do.
//
// The /clear rotation and the idle/cap eviction are ONE row rather than two on
// purpose: transitionClearsTurn is what routes both to this single
// clearForSession call, and TestTurnBusyTracker_ClearForSession already pins
// that routing.
func TestTurnBusyTracker_ToolCallDropSignals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		drop func(*turnBusyTracker)
	}{
		{"the stream says the call finished", func(tr *turnBusyTracker) {
			tr.observe("sess-a", turnevent.ToolUpdate{ToolCallID: "tu-a1", Status: turnevent.ToolStatusCompleted})
		}},
		{"the turn ends", func(tr *turnBusyTracker) {
			tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
		}},
		{"the turn is cancelled", func(tr *turnBusyTracker) {
			tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled})
		}},
		{"the session is torn down", func(tr *turnBusyTracker) {
			tr.clearForSession("sess-a")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := newTurnBusyTracker(stubBusyResolve(map[string]string{
				"sess-a": testConvID,
				"sess-b": testConvIDB,
			}), discardLogger())

			tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})
			tr.observe("sess-b", turnevent.ToolStart{ToolCallID: "tu-b1", Title: "Read"})
			if !tr.ToolCallInFlight(testConvID, "tu-a1") {
				t.Fatal("ToolCallInFlight(A, tu-a1) = false before the drop; the assertion below would pass vacuously")
			}

			tc.drop(tr)

			if tr.ToolCallInFlight(testConvID, "tu-a1") {
				t.Error("ToolCallInFlight(A, tu-a1) = true after the drop, want false")
			}
			if !tr.ToolCallInFlight(testConvIDB, "tu-b1") {
				t.Error("ToolCallInFlight(B, tu-b1) = false; the drop reached a conversation it was not for")
			}
		})
	}
}

// #1917 AC1 (the mid-turn case, which the ordering inside setBusy is what makes
// work): a ToolStart landing on an ALREADY-BUSY conversation is still captured.
// The tool delta is applied before setBusy's membership early return precisely
// for this — open == was fires that return for every tool call but the first of a
// turn, so a delta applied after it would capture none of them.
func TestTurnBusyTracker_ToolCallCapturedMidTurn(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())

	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})
	if !tr.Busy(testConvID) {
		t.Fatal("Busy = false after an opener; the tool call below would not be arriving mid-turn")
	}

	tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-1", Title: "Read"})
	if !tr.ToolCallInFlight(testConvID, "tu-1") {
		t.Error("ToolCallInFlight = false for a tool call started mid-turn, want true")
	}
}

// #1917: toolCallDeltaFor is pure and switches on the Go variant type only.
//
// The ToolUpdate rows are one per ToolStatus value, all expecting the SAME drop
// — that is what pins "Status is deliberately not read" and reddens a later
// `switch upd.Status` refinement. Reading it would break the discipline
// turnMarkFor states, that no content claude produced steers the answer, and
// would fail OPEN: a fabricated in-progress tool_result would pin a finished
// call in flight instead of dropping it. In production a ToolUpdate is always
// terminal anyway — the parser emits it from one site, emitUser, and toolStatus
// maps is_error onto completed/failed only.
//
// The PermissionRequest row is the interesting negative: it is the one other
// variant carrying a ToolCallID, and it must still contribute no delta.
func TestToolCallDeltaFor_ClassifiesToolVariantsOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   turnevent.Event
		want toolCallDelta
	}{
		{"tool_start adds", turnevent.ToolStart{ToolCallID: "tu-1", Title: "Read"}, toolCallDelta{id: "tu-1", started: true}},
		{"tool_update drops (pending)", turnevent.ToolUpdate{ToolCallID: "tu-1", Status: turnevent.ToolStatusPending}, toolCallDelta{id: "tu-1"}},
		{"tool_update drops (in_progress)", turnevent.ToolUpdate{ToolCallID: "tu-1", Status: turnevent.ToolStatusInProgress}, toolCallDelta{id: "tu-1"}},
		{"tool_update drops (completed)", turnevent.ToolUpdate{ToolCallID: "tu-1", Status: turnevent.ToolStatusCompleted}, toolCallDelta{id: "tu-1"}},
		{"tool_update drops (failed)", turnevent.ToolUpdate{ToolCallID: "tu-1", Status: turnevent.ToolStatusFailed}, toolCallDelta{id: "tu-1"}},
		// The ID alone is the discriminant — an empty one is no delta whatever
		// `started` says, which is why setBusy tests `tool.id != ""` and never the
		// zero VALUE. Asserting the zero value here instead would pin a normalisation
		// the classifier deliberately does not perform, so this row is named for what
		// it actually checks: that the classifier passes the empty id THROUGH. That
		// the empty id then carries no delta is setBusy's refusal, pinned in
		// TestTurnBusyTracker_ToolCallInFlightNegativesCollapse.
		{"tool_start with no id passes the empty id through unnormalised", turnevent.ToolStart{Title: "Read"}, toolCallDelta{started: true}},
		{"text_chunk carries no delta", turnevent.TextChunk{MessageID: "m1", Text: "hello"}, toolCallDelta{}},
		{"turn_end carries no delta", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, toolCallDelta{}},
		{"permission_request carries no delta", turnevent.NewPermissionRequest("req-1", "tu-1", "Proceed?", nil), toolCallDelta{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := toolCallDeltaFor(tc.ev); got != tc.want {
				t.Errorf("toolCallDeltaFor(%T) = %+v, want %+v", tc.ev, got, tc.want)
			}
		})
	}
}

// #1917 AC4: no log line is added on any tool-call path.
//
// ZERO records rather than a substring scan over forbidden fields. A scan has to
// enumerate what it forbids and goes vacuous the day a diagnostic names
// something new; the count reddens on any added line whatever it carries. The
// tool's name and its raw input are both present on the fed event, so a
// diagnostic that echoed either would be caught.
//
// The session RESOLVES, so the pre-existing stream_turn.busy_unresolved line —
// content-free, and not this criterion's subject — is not what is being measured.
func TestTurnBusyTracker_ToolCallPathLogsNothing(t *testing.T) {
	t.Parallel()

	recs := make(chan slog.Record, 8)
	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}),
		slog.New(dropWatcher{recs: recs}))

	tr.observe("sess-a", turnevent.ToolStart{
		ToolCallID: "tu-1",
		Title:      "Bash",
		RawInput:   json.RawMessage(`{"command":"echo hi"}`),
	})
	if !tr.ToolCallInFlight(testConvID, "tu-1") {
		t.Fatal("ToolCallInFlight = false after a ToolStart; the record count below would be measuring nothing")
	}
	tr.observe("sess-a", turnevent.ToolUpdate{ToolCallID: "tu-1", Status: turnevent.ToolStatusCompleted})
	_ = tr.ToolCallInFlight(testConvID, "tu-1")
	// The empty-id refusal is silent too — the classification arm most likely to
	// attract a diagnostic later.
	tr.observe("sess-a", turnevent.ToolStart{Title: "Read"})

	if n := len(recs); n != 0 {
		t.Errorf("the tool-call path emitted %d log record(s), want 0", n)
	}
}

// --- #1199: the delivery feed -------------------------------------------------

// busyGeneration reads the tracker's current generation channel. Channel IDENTITY
// is the only observable for "did a broadcast happen?": setBusy closes and
// REPLACES this channel exactly when membership moves, and a spurious wakeup is
// behaviourally invisible to a waiter (it re-checks its key and re-parks), so a
// test asserting "no spurious broadcast" has to compare the channel itself.
func busyGeneration(tr *turnBusyTracker) chan struct{} {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.changed
}

// #1199 AC1: the two nil-safety contracts the wiring depends on. In PTY mode the
// tracker is never constructed, so the delivery seam calls BOTH new methods on a
// nil receiver on every single delivery — that is the path that must stay
// semantically identical to the pre-#1199 body, not a defensive nicety. The empty
// conversation id is the second half: refusing it preserves the
// absent ≡ idle ≡ unknown ≡ unbound collapse (a tracked empty key would both wedge
// that key and collide with the unknown-conversation answer).
func TestTurnBusyTracker_DeliveryFeedNilAndEmptyAreNoOps(t *testing.T) {
	t.Parallel()

	// The two sub-cases are written out rather than tabled because their assertions
	// genuinely differ: Busy and WaitIdle carry NO nil guard (they take t.mu
	// immediately, as their docs record), so the nil case can only assert "returns,
	// and does not panic" — reading back through Busy is exactly the mistake the
	// wiring must not make.
	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var tr *turnBusyTracker

		if err := tr.waitIdleForDelivery(context.Background(), testConvID, time.Nanosecond); err != nil {
			t.Errorf("waitIdleForDelivery = %v, want nil (immediate, not even a deadline)", err)
		}
		undo := tr.openForDelivery(testConvID)
		if undo == nil {
			t.Fatal("openForDelivery returned a nil undo; the seam calls it unconditionally on the write-error path")
		}
		undo() // must not panic
	})

	t.Run("empty conversation id", func(t *testing.T) {
		t.Parallel()
		tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())

		if err := tr.waitIdleForDelivery(context.Background(), "", time.Nanosecond); err != nil {
			t.Errorf("waitIdleForDelivery = %v, want nil (immediate, not even a deadline)", err)
		}
		undo := tr.openForDelivery("")
		if undo == nil {
			t.Fatal("openForDelivery returned a nil undo")
		}
		undo() // must not panic
		if tr.Busy("") {
			t.Error(`Busy("") = true; the empty key must never be tracked`)
		}
	})
}

// #1199 AC1: openForDelivery marks the conversation mid-turn, and its undo both
// clears the mark and WAKES a parked waiter. The wake half is what fails if the
// undo mutates the map without going through setBusy's close-and-replace
// broadcast: Busy would already read idle while every waiter stayed parked on a
// generation channel nobody closed — the lost-wakeup bug, reached here through the
// write-error path rather than through a teardown.
func TestTurnBusyTracker_OpenForDeliveryMarksAndUndoWakesWaiter(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())

	undo := tr.openForDelivery(testConvID)
	if !tr.Busy(testConvID) {
		t.Fatal("Busy = false after openForDelivery, want true")
	}

	done := make(chan error, 1)
	go func() { done <- tr.WaitIdle(context.Background(), testConvID) }()

	// The wait must still be outstanding while the delivery's turn is open. This
	// grace window can never fail a correct WaitIdle; it only fires on an early
	// return.
	select {
	case err := <-done:
		t.Fatalf("WaitIdle returned (%v) while the delivery's mark was live", err)
	case <-time.After(50 * time.Millisecond):
	}

	undo()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitIdle = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitIdle never returned after the delivery's undo; the undo skipped setBusy's broadcast")
	}
	if tr.Busy(testConvID) {
		t.Error("Busy = true after the undo, want false")
	}
}

// #1199 AC1 (the teeth): the undo is scoped to the open THIS call made.
//
// Two sub-cases, and only a setBusy-reported `changed` satisfies both. An undo
// that unconditionally clears passes neither: in "already busy" it would report a
// turn some other feed owns as idle — the TOCTOU the design closes, whose
// consequence is the next message going through unheld, i.e. a transient
// recurrence of the very bug this ticket fixes. In "already cleared" it would
// re-broadcast on an idle conversation.
//
// "already busy" is reachable in production: an opener event landing in the gap
// between waitIdleForDelivery returning nil and the mark (a --resume respawn
// replaying events is the plausible route).
func TestTurnBusyTracker_UndoScopedToItsOwnOpen(t *testing.T) {
	t.Parallel()

	t.Run("already busy: the undo does not clear another feed's turn", func(t *testing.T) {
		t.Parallel()
		tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())

		tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})
		if !tr.Busy(testConvID) {
			t.Fatal("Busy = false after an opener; the sub-case would be vacuous")
		}

		undo := tr.openForDelivery(testConvID) // opened nothing — already busy
		undo()

		if !tr.Busy(testConvID) {
			t.Error("Busy = false; the undo cleared a turn this delivery never opened")
		}
	})

	t.Run("already cleared: the undo neither mutates nor re-broadcasts", func(t *testing.T) {
		t.Parallel()
		tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())

		undo := tr.openForDelivery(testConvID)
		tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
		if tr.Busy(testConvID) {
			t.Fatal("Busy = true after TurnEnd; the sub-case would be vacuous")
		}

		gen := busyGeneration(tr)
		undo() // must not panic, must not broadcast
		if tr.Busy(testConvID) {
			t.Error("Busy = true after an undo on an already-idle conversation")
		}
		if busyGeneration(tr) != gen {
			t.Error("the undo replaced the generation channel on an already-idle conversation; a no-op must not broadcast")
		}
	})
}

// #1199 AC1: waitIdleForDelivery returns as soon as the conversation clears. The
// clear arrives here through observe — the ordinary production feed — so this is
// the mid-turn hold's happy path end to end at the tracker tier.
func TestTurnBusyTracker_WaitIdleForDeliveryReturnsOnClear(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})

	done := make(chan error, 1)
	go func() { done <- tr.waitIdleForDelivery(context.Background(), testConvID, 5*time.Second) }()

	select {
	case err := <-done:
		t.Fatalf("waitIdleForDelivery returned (%v) while the turn was open", err)
	case <-time.After(50 * time.Millisecond):
	}

	tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("waitIdleForDelivery = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitIdleForDelivery never returned after the conversation's TurnEnd")
	}
}

// #1199 AC3 (tracker tier): a turn that never ends bounds the wait rather than
// holding it forever, and reports DeadlineExceeded — the classification the
// delivery seam wraps and msgqueue's give-up path then counts. The conversation is
// still busy afterwards: the deadline reports that THIS waiter gave up, never that
// the turn ended.
func TestTurnBusyTracker_WaitIdleForDeliveryTimesOut(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})

	err := tr.waitIdleForDelivery(context.Background(), testConvID, 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waitIdleForDelivery on a never-ending turn = %v, want %v", err, context.DeadlineExceeded)
	}
	if !tr.Busy(testConvID) {
		t.Error("Busy = false after the bounded wait expired; a deadline must not clear the turn")
	}
}

// #1199 AC2's mechanism at the tracker tier: a cancelled parent ctx returns
// context.Canceled promptly, which is how BOTH a dropped head (msgqueue.Remove
// cancels the in-flight delivery so this wait unblocks at once) and daemon
// shutdown escape the hold. The timeout here is deliberately far larger than the
// test's patience, so passing requires the cancel path, not the deadline.
func TestTurnBusyTracker_WaitIdleForDeliveryHonoursCancel(t *testing.T) {
	t.Parallel()

	tr := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "hello"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.waitIdleForDelivery(ctx, testConvID, time.Hour) }()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waitIdleForDelivery after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitIdleForDelivery never returned after its context was cancelled")
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

// --- #1209: the exit lane on the fan-in ---------------------------------------

// exitLaneDrain wires the drain for the two ordering tests below: the cursor and
// the gate both on conversation B, while the exits and openers under test are all
// for session A. That fixture is what makes the exit arm's PLACEMENT load-bearing
// — an exit handled after the active-session gate would be dropped there and never
// clear a BACKGROUND conversation, which is the common case for a crash (the
// crashed runner need not be the one the user is looking at). A test run entirely
// on the active session cannot tell the two placements apart.
func exitLaneDrain(t *testing.T) (sink *streamTurnSink, busy *turnBusyTracker, drops chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cur := &stubCursor{}
	cur.set(testConvIDB) // cursor on conversation B...
	active := &stubActiveSession{}
	active.set("sess-b") // ...and B's bound session is what the gate admits
	bcast := newChanBcast("conn-b")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	busy = newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
		"sess-b": testConvIDB,
	}), discardLogger())

	drops = make(chan string, 8)
	sink = newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, busy,
		slog.New(dropWatcher{kinds: drops}))
	t.Cleanup(func() { cancel(); cleanup() }) // cancel-then-join; joining first deadlocks
	return sink, busy, drops
}

// #1209 AC2: [opener for S, exit for S] pushed in that order leaves S idle.
//
// NO result line is fed, so no TurnEnd is ever parsed for this turn, and no
// transition observer exists anywhere in this test. Those are exactly the two
// feeds that are structurally silent when a child crashes mid-turn, so the clear
// asserted here can only have come from the exit lane.
func TestStreamTurnDrainV2_ExitClearsOpenTurn(t *testing.T) {
	t.Parallel()

	sink, busy, drops := exitLaneDrain(t)

	feedLines(sink, "sess-a", assistantTextLine("ma", "for-A"))

	// Barrier: the not-active drop is logged AFTER observe on the same goroutine.
	waitDropKind(t, drops, "text_chunk")

	// NOT decoration. WaitIdle returns nil IMMEDIATELY on an already-idle
	// conversation, so without establishing busy first the wait below would pass
	// even with the exit lane doing nothing at all — the drain simply might not
	// have reached the opener yet, leaving A idle for the wrong reason.
	if !busy.Busy(testConvID) {
		t.Fatalf("Busy(A) = false after an opener; the WaitIdle below would then pass vacuously")
	}

	sink.exitFor("sess-a")()

	// Barrier: the clear's setBusy closes t.changed, which is what wakes WaitIdle
	// — the one barrier an exit envelope offers, since its arm continues before
	// the gate's drop log and before any push.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := busy.WaitIdle(waitCtx, testConvID); err != nil {
		t.Fatalf("WaitIdle(A) = %v after a child-exit signal for A's session, want nil", err)
	}
	if busy.Busy(testConvID) {
		t.Errorf("Busy(A) = true after the exit closed A's abandoned turn, want false")
	}
}

// #1209 AC3: [exit for S, opener for S] pushed in that order leaves S busy — the
// exit does not clear a turn opened after it. This is the criterion that forbids a
// deferred or asynchronous clear: a clear handed to a goroutine would satisfy AC2
// and fail here.
func TestStreamTurnDrainV2_ExitDoesNotClearALaterTurn(t *testing.T) {
	t.Parallel()

	sink, busy, drops := exitLaneDrain(t)

	sink.exitFor("sess-a")() // ...before any turn on A exists
	feedLines(sink, "sess-a", assistantTextLine("ma", "for-A"))

	// Barrier: the trailing opener's own not-active drop. The drain is serial and
	// FIFO, so this proves the exit was processed first.
	waitDropKind(t, drops, "text_chunk")

	if !busy.Busy(testConvID) {
		t.Fatalf("Busy(A) = false; an exit that precedes the opener must not clear it")
	}

	// The Busy check alone catches a GOROUTINE-dispatched clear only sometimes —
	// it could land either side of the FIFO barrier. A bounded wait wanting a
	// deadline is the assertion that fails deterministically for that design. The
	// flake direction is safe: a correct implementation always times out, so
	// machine slowness makes this more likely to pass, never to fail spuriously.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer shortCancel()
	if err := busy.WaitIdle(shortCtx, testConvID); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitIdle(A) = %v, want %v; the exit was dispatched asynchronously and landed late",
			err, context.DeadlineExceeded)
	}
}

// #1209 AC3 (the teeth): the clear runs INLINE on the drain goroutine, before the
// next envelope on the fan-in is processed.
//
// The test above states AC3's criterion, but it catches a goroutine-dispatched
// clear only by luck: with [exit, opener] the deferred clear fires while the
// conversation is still idle, so it is a harmless no-op unless the goroutine is
// delayed past the opener. Here the exit lands on a BUSY conversation and the
// barrier is a LATER envelope's drop, so the ordering is what is asserted: at the
// moment the drain logs C's drop it has already returned from the exit arm, and a
// synchronous clear has provably taken effect. `go busy.clearForSession(…)` fails
// this unless the fresh goroutine outruns the drain's very next loop iteration.
// The flake direction is safe — a correct inline clear has ALWAYS taken effect at
// this barrier, so there is no timing under which this fails spuriously.
func TestStreamTurnDrainV2_ExitClearsInlineBeforeTheNextEnvelope(t *testing.T) {
	t.Parallel()

	sink, busy, drops := exitLaneDrain(t)

	feedLines(sink, "sess-a", assistantTextLine("ma", "for-A"))
	waitDropKind(t, drops, "text_chunk")
	if !busy.Busy(testConvID) {
		t.Fatalf("Busy(A) = false after an opener; the clear below would have nothing to do")
	}

	sink.exitFor("sess-a")()

	// A trailing event for a third, unresolvable session: it is tracked nowhere and
	// dropped at the gate, so its only role is to make the drain log one more time
	// AFTER it has finished with the exit.
	feedLines(sink, "sess-c", assistantTextLine("mc", "for-C"))
	waitDropKind(t, drops, "text_chunk")

	if busy.Busy(testConvID) {
		t.Errorf("Busy(A) = true once a later envelope had already been processed; the clear did not run inline on the drain goroutine")
	}
}

// #1917 AC3 (last clause): a child that dies mid-turn drops the tool calls it
// left in flight. Drain tier, on the exit lane, so the drop can only have come
// from there: no result line is fed, so no TurnEnd is ever parsed for this turn,
// and no transition observer exists anywhere in this fixture.
//
// The line is the real tool_use block, through the real parser — the reachable
// input the unit-tier tables stand in for.
func TestStreamTurnDrainV2_ExitDropsInFlightToolCall(t *testing.T) {
	t.Parallel()

	sink, busy, drops := exitLaneDrain(t)

	feedLines(sink, "sess-a", toolUseLine)

	// Barrier: the not-active drop is logged AFTER observe on the same goroutine,
	// so seeing it proves the tracker was already fed. No sleep.
	waitDropKind(t, drops, "tool_start")

	// NOT decoration, and it has to come FIRST: a tracker that captured nothing at
	// all reports exactly the same false as one that dropped correctly, so without
	// this the negative below passes vacuously.
	if !busy.ToolCallInFlight(testConvID, "tu-1") {
		t.Fatalf("ToolCallInFlight(A, tu-1) = false after A's tool_use block; the assertion below would pass vacuously")
	}

	sink.exitFor("sess-a")()

	// Barrier: the clear's setBusy closes t.changed, which is what wakes WaitIdle
	// — the one barrier an exit envelope offers, since its arm continues before
	// the gate's drop log and before any push.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := busy.WaitIdle(waitCtx, testConvID); err != nil {
		t.Fatalf("WaitIdle(A) = %v after a child-exit signal for A's session, want nil", err)
	}
	if busy.ToolCallInFlight(testConvID, "tu-1") {
		t.Errorf("ToolCallInFlight(A, tu-1) = true after the exit closed A's abandoned turn, want false")
	}
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
