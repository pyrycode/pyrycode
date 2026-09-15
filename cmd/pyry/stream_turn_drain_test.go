package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// AC3 (no transcript on the stream path) is asserted structurally: nothing in
// this file — or in stream_turn_drain.go — does any filesystem setup, opens a
// <uuid>.jsonl, or imports fsnotify / a jsonl resolver. The only input the drain
// consumes is stream-json BYTES fed through a streamsup.Parser; the absence of
// any file machinery is the assertion.

// testConvIDB is a second valid conversation id, distinct from testConvID
// (interactive_turn_v2_test.go), for the AC2 cross-conversation scoping tests.
const testConvIDB = "22222222-2222-4222-8222-222222222222"

// stubActiveSession is a race-safe activeSession test double: set() stores the
// current active session id, get() returns it plus ok = (id != ""). Modelled on
// stubCursor; the drain reads it concurrently with the test goroutine's set().
type stubActiveSession struct {
	mu sync.Mutex
	id string
}

func (s *stubActiveSession) set(id string) { s.mu.Lock(); s.id = id; s.mu.Unlock() }

func (s *stubActiveSession) get() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id, s.id != ""
}

// chanBcast is a channel-based interactiveBroadcaster double for the drain tests.
// Unlike fakeInteractiveBcast (built for the emitter's single-test-goroutine
// callers), the drain calls emit on ITS OWN goroutine, so Push forwards each
// envelope over a channel — the send/receive edge synchronises the drain
// goroutine with the collecting test goroutine (no data race, deterministic
// order). Only interactive conns matter; the fixed conn set is read-only.
type chanBcast struct {
	conns  []relay.ActiveConn
	pushed chan protocol.Envelope
}

func newChanBcast(connID string) *chanBcast {
	return &chanBcast{
		conns:  []relay.ActiveConn{{ConnID: connID, Interactive: true}},
		pushed: make(chan protocol.Envelope, 128),
	}
}

func (b *chanBcast) ActiveConns(ctx context.Context) []relay.ActiveConn { return b.conns }

func (b *chanBcast) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	b.pushed <- env
	return nil
}

// dropWatcher is a slog.Handler that signals the drain's content-free not-active
// drops, carrying the event's "kind", so a negative test can barrier on the last
// fed event's drop (guaranteeing every earlier event was processed first — the
// drain is serial) before asserting nothing was pushed.
//
// recs is the second, optional forward: the WHOLE record, for assertions on a
// diagnostic that carries no "kind" at all — the exit-lane drop (#1209), whose
// level and exact field set are the assertion. Both channels are optional; a nil
// channel inside a select with a default simply takes the default, so every
// existing dropWatcher{kinds: …} construction keeps behaving identically.
type dropWatcher struct {
	kinds chan string
	recs  chan slog.Record
}

func (dropWatcher) Enabled(context.Context, slog.Level) bool { return true }

func (h dropWatcher) Handle(_ context.Context, r slog.Record) error {
	var event, kind string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "event":
			event = a.Value.String()
		case "kind":
			kind = a.Value.String()
		}
		return true
	})
	if event == "stream_turn.not_active" {
		select {
		case h.kinds <- kind:
		default:
		}
	}
	// Cloned: a slog.Record must not be retained past Handle without it. The
	// event-name filtering is left to the test, which is what keeps this forward
	// usable for any record shape.
	select {
	case h.recs <- r.Clone():
	default:
	}
	return nil
}

func (h dropWatcher) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h dropWatcher) WithGroup(string) slog.Handler      { return h }

// --- stream-json line fixtures --------------------------------------------

func assistantTextLine(msgID, text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"role":"assistant","content":[{"type":"text","text":%q}]}}`, msgID, text)
}

const (
	resultLine   = `{"type":"result","subtype":"success","session_id":"S"}`
	toolUseLine  = `{"type":"assistant","message":{"id":"m-tool","role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Read","input":{"file_path":"/tmp/x"}}]}}`
	toolRsltLine = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"ok","is_error":false}]}}`
)

// feedLines pushes each line (newline-terminated) through a fresh Parser whose
// sink is bound to sessionID — the exact wiring newStreamRunnerFactory installs.
func feedLines(sink *streamTurnSink, sessionID string, lines ...string) {
	p := streamsup.NewParser(sink.sinkFor(sessionID), discardLogger())
	for _, ln := range lines {
		_, _ = p.Write([]byte(ln + "\n"))
	}
}

// feedLinesTagged is feedLines through the LIVE-tag handle (#1133): the parser is
// built once, against a tag the caller can rotate between calls, which is the
// production shape newStreamRunnerFactory installs. feedLines' frozen-string form
// is kept beside it because most tests never rotate and the constant tag reads
// better there.
func feedLinesTagged(sink *streamTurnSink, tag *streamSessionTag, lines ...string) {
	p := streamsup.NewParser(sink.sinkForTag(tag.ID), discardLogger())
	for _, ln := range lines {
		_, _ = p.Write([]byte(ln + "\n"))
	}
}

// --- collection helpers ---------------------------------------------------

func collectEnvs(t *testing.T, ch <-chan protocol.Envelope, n int) []protocol.Envelope {
	t.Helper()
	out := make([]protocol.Envelope, 0, n)
	deadline := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case env := <-ch:
			out = append(out, env)
		case <-deadline:
			t.Fatalf("timed out after %d/%d envelopes: %v", len(out), n, envTypes(out))
		}
	}
	return out
}

func envTypes(envs []protocol.Envelope) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Type
	}
	return out
}

func decodeDelta(t *testing.T, env protocol.Envelope) protocol.AssistantDeltaPayload {
	t.Helper()
	var d protocol.AssistantDeltaPayload
	if err := json.Unmarshal(env.Payload, &d); err != nil {
		t.Fatalf("decode assistant_delta: %v", err)
	}
	return d
}

// dropKindWaitTimeout is waitDropKind's give-up budget. Measured 2026-08-25 under
// synthetic contention (the compiled -race binary at GOMAXPROCS=2 alongside 8
// busy-loop CPU hogs, 15 runs): every run passed, but the slowest wait took 1.53s
// against the then-budget of 2s — a headroom multiple of 1.3×, under a load
// lighter than a real full-suite fan-out, so that tail is an underestimate. 10s
// buys 6.5× and turns a red here back into evidence of a regression in the exit
// lane rather than of a busy machine.
//
// 10s rather than something smaller because it is not an invented number:
// TestStreamRunnerFactory_ChildExitClearsTurnBusy — the call site that actually
// reddened the gate — already arms a 10s WaitIdle on the same spawned-child
// fixture, so 10s is already this package's stated tolerance for that lane. The
// larger value costs nothing on the green path: the helper returns the instant the
// wanted kind arrives, so the budget is paid only on a genuine red. It stays far
// under Go's default package timeout, which is what keeps a genuine hang surfacing
// as a named failure here rather than as a whole-package panic dump.
const dropKindWaitTimeout = 10 * time.Second

func waitDropKind(t *testing.T, kinds <-chan string, want string) {
	t.Helper()
	start := time.Now()
	deadline := time.After(dropKindWaitTimeout)
	// seen is best-effort and in arrival order, duplicates kept: dropWatcher's send
	// is non-blocking, so a kind can be discarded on a full channel and never reach
	// here. At the deadline an empty slice says the wanted kind never arrived, a
	// non-empty one says the wrong kinds kept coming — which is the whole point of
	// reporting it. The elapsed is not redundant with the budget either: ~10s means
	// the budget bound, far more means this goroutine was starved out of its select.
	var seen []string
	for {
		select {
		case k := <-kinds:
			seen = append(seen, k)
			if k == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out after %v waiting for a not-active drop of kind %q; kinds seen: %v",
				time.Since(start).Round(time.Millisecond), want, seen)
		}
	}
}

func assertNoPush(t *testing.T, ch <-chan protocol.Envelope) {
	t.Helper()
	select {
	case env := <-ch:
		t.Fatalf("expected no envelope pushed, got %s", env.Type)
	default:
	}
}

// --- tests ----------------------------------------------------------------

// AC1: a stream-json session drives turn_state + assistant_delta through the
// UNCHANGED emitter (reused Handle entry point), stamped with the active
// conversation id. One assistant text line + one result line flush
// deterministically at the result→TurnEnd boundary (no timer).
func TestStreamTurnDrainV2_FullSingleTurn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set("sess-a")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil, discardLogger())
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	feedLines(sink, "sess-a", assistantTextLine("m1", "hello"), resultLine)

	got := collectEnvs(t, bcast.pushed, 4)
	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // hello
		protocol.TypeTurnEnd,        // end_turn
		protocol.TypeTurnState,      // idle
	}
	if !slices.Equal(envTypes(got), wantTypes) {
		t.Fatalf("envelope types:\n got %v\nwant %v", envTypes(got), wantTypes)
	}
	d := decodeDelta(t, got[1])
	if d.Text != "hello" {
		t.Errorf("assistant_delta text = %q, want %q", d.Text, "hello")
	}
	if d.ConversationID != testConvID {
		t.Errorf("assistant_delta conversation_id = %q, want %q", d.ConversationID, testConvID)
	}
}

func TestStreamTurnDrainV2_AttributedTextExcludesThinkingAndSignature(t *testing.T) {
	t.Parallel()
	const (
		parentID        = "toolu-agent-2331"
		visibleText     = "VISIBLE-SUBAGENT-TEXT-2331"
		thinkingSecret  = "PRIVATE-SUBAGENT-THINKING-2331"
		signatureSecret = "PRIVATE-SUBAGENT-SIGNATURE-2331"
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set("sess-a")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil, discardLogger())
	defer func() { cancel(); cleanup() }()

	attributed := `{"type":"assistant","parent_tool_use_id":"` + parentID +
		`","message":{"id":"m-child","role":"assistant","content":[` +
		`{"type":"thinking","thinking":"` + thinkingSecret + `","signature":"` + signatureSecret + `"},` +
		`{"type":"text","text":"` + visibleText + `"}]}}`
	feedLines(sink, "sess-a", attributed, resultLine)

	got := collectEnvs(t, bcast.pushed, 5)
	var delta protocol.AssistantDeltaPayload
	foundDelta := false
	for _, env := range got {
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal captured %s envelope: %v", env.Type, err)
		}
		for _, secret := range []string{thinkingSecret, signatureSecret} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s envelope published confidential marker %q: %s", env.Type, secret, raw)
			}
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue
		}
		if err := json.Unmarshal(env.Payload, &delta); err != nil {
			t.Fatalf("decode attributed assistant_delta: %v", err)
		}
		foundDelta = true
	}
	if !foundDelta {
		t.Fatal("ordinary attributed text did not reach an assistant_delta")
	}
	if delta.Text != visibleText || delta.ParentToolUseID != parentID {
		t.Errorf("assistant_delta = {text:%q parent:%q}, want {%q %q}",
			delta.Text, delta.ParentToolUseID, visibleText, parentID)
	}
}

// AC2: a background conversation's parser events (its session != the active
// session) are dropped BEFORE the emitter, so its conn never receives them.
func TestStreamTurnDrainV2_ScopingDropsBackground(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID) // active conversation A
	active := &stubActiveSession{}
	active.set("sess-a")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	drops := make(chan string, 8)
	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil,
		slog.New(dropWatcher{kinds: drops}))
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	// Feed session B's bytes while A is active — every event must drop.
	feedLines(sink, "sess-b", assistantTextLine("mb", "SECRET-B"), resultLine)

	// Barrier: the TurnEnd is the last fed event; observing its not-active drop
	// proves the earlier TextChunk was processed too (the drain is serial).
	waitDropKind(t, drops, "turn_end")
	assertNoPush(t, bcast.pushed)
}

// AC2: once a conversation is active, its bound session's events ARE forwarded,
// stamped with that conversation's id (the switch/foreground half).
func TestStreamTurnDrainV2_ScopingForwardsActiveStamped(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvIDB) // active conversation B
	active := &stubActiveSession{}
	active.set("sess-b")
	bcast := newChanBcast("conn-b")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil, discardLogger())
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	feedLines(sink, "sess-b", assistantTextLine("mb", "for-B"), resultLine)

	got := collectEnvs(t, bcast.pushed, 4)
	for _, env := range got {
		var cid string
		switch env.Type {
		case protocol.TypeAssistantDelta:
			cid = decodeDelta(t, env).ConversationID
		case protocol.TypeTurnState:
			var ts protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &ts); err != nil {
				t.Fatalf("decode turn_state: %v", err)
			}
			cid = ts.ConversationID
		default:
			continue
		}
		if cid != testConvIDB {
			t.Errorf("%s stamped conversation_id = %q, want %q", env.Type, cid, testConvIDB)
		}
	}
	if d := decodeDelta(t, got[1]); d.Text != "for-B" {
		t.Errorf("assistant_delta text = %q, want %q", d.Text, "for-B")
	}
}

// AC2 + concurrency: two parsers (sess-a active, sess-b background) feed the sink
// concurrently. Only the sole drain goroutine touches the emitter, so -race must
// stay clean, and session B's content must never leak to A's conn.
func TestStreamTurnDrainV2_ConcurrentFeedSingleWriter(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set("sess-a")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil, discardLogger())
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); feedLines(sink, "sess-b", assistantTextLine("mb", "SECRET-B"), resultLine) }()
	go func() { defer wg.Done(); feedLines(sink, "sess-a", assistantTextLine("ma", "alpha"), resultLine) }()
	wg.Wait()

	// A's four envelopes arrive; B's are dropped at the gate. Collecting exactly
	// four and finding no B content proves the scoping held under concurrency.
	got := collectEnvs(t, bcast.pushed, 4)
	for _, env := range got {
		if env.Type == protocol.TypeAssistantDelta {
			d := decodeDelta(t, env)
			if strings.Contains(d.Text, "SECRET-B") {
				t.Fatalf("session B content leaked to A's conn: %q", d.Text)
			}
			if d.Text != "alpha" {
				t.Errorf("assistant_delta text = %q, want %q", d.Text, "alpha")
			}
		}
	}
}

// The drain gate drops every event when no conversation is active (activeSession
// returns ok=false) — the stream analogue of the emitter's empty-cursor drop.
func TestStreamTurnDrainV2_NoActiveSessionDrops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{} // never set → ok=false
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	drops := make(chan string, 8)
	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil,
		slog.New(dropWatcher{kinds: drops}))
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	feedLines(sink, "sess-a", assistantTextLine("m1", "hello"), resultLine)

	waitDropKind(t, drops, "turn_end")
	assertNoPush(t, bcast.pushed)
}

// The drain drives the emitter's ~250ms coalescing timer: a lone assistant text
// line (no following event) still surfaces its assistant_delta once the timer
// fires — proving the drain selects flushC() and calls flushDelta.
func TestStreamTurnDrainV2_FlushTimerCoalesces(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set("sess-a")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil, discardLogger())
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	feedLines(sink, "sess-a", assistantTextLine("m1", "streamed"))

	// responding first, then the coalesced delta once the ~250ms window elapses.
	got := collectEnvs(t, bcast.pushed, 2)
	if got[0].Type != protocol.TypeTurnState {
		t.Fatalf("first envelope = %s, want %s", got[0].Type, protocol.TypeTurnState)
	}
	if got[1].Type != protocol.TypeAssistantDelta {
		t.Fatalf("second envelope = %s, want %s (timer-flushed)", got[1].Type, protocol.TypeAssistantDelta)
	}
	if d := decodeDelta(t, got[1]); d.Text != "streamed" {
		t.Errorf("coalesced assistant_delta text = %q, want %q", d.Text, "streamed")
	}
}

// --- #1209: the exit lane ----------------------------------------------------

// #1209 AC4: an exit envelope produces NO emitter traffic — its arm continues
// before observe, before the active-session gate and before Handle — and a nil
// tracker stays a no-op, which is what lets the drain's pre-#1201 wiring (busy =
// nil) keep working.
//
// The barrier is the TRAILING event's own drop, not the exit's: an exit envelope
// is barrier-less by construction (no push, no not-active drop, nothing). The
// drain is serial and FIFO, so seeing the later event's drop proves the exit was
// already fully processed — the trick this file's dropWatcher doc records above,
// and the only option for proving a no-op exit was processed at all.
func TestStreamTurnDrainV2_ExitProducesNoEmitterTraffic(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{} // never set → ok=false, so the trailing event drops at the gate
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	drops := make(chan string, 8)
	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil,
		slog.New(dropWatcher{kinds: drops}))
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	sink.exitFor("sess-a")() // an exit against a nil tracker: must not panic
	feedLines(sink, "sess-a", assistantTextLine("ma", "hello"))

	waitDropKind(t, drops, "text_chunk")
	assertNoPush(t, bcast.pushed)
}

// #1209 AC1: the exit drop's LEVEL and exact field set. A dropped exit is a
// permanently wedged conversation once a producer is wired, where a dropped event
// is a lost delta — so this one is Warn (visible at the daemon's default
// LevelInfo) while the siblings stay Debug. The record carries the discriminant
// and the session id and nothing else: no event content, and no "kind", there
// being no event to name.
//
// No drain is started: with no reader the buffer-of-1 fill is deterministic and
// timing-free. Note the watcher goes on the SINK's logger — the exit drop is
// emitted from the sink closure, never from the drain.
func TestStreamTurnSink_ExitDropWhenFull(t *testing.T) {
	t.Parallel()

	recs := make(chan slog.Record, 8)
	// newStreamTurnSink only replaces buf when it is <= 0, so 1 survives.
	sink := newStreamTurnSink(1, slog.New(dropWatcher{recs: recs}))

	sink.exitFor("sess-a")() // occupies the single slot, silently
	sink.exitFor("sess-a")() // no room: dropped and logged

	var rec slog.Record
	select {
	case rec = <-recs:
	default:
		t.Fatal("no record logged for an exit dropped on a full sink")
	}

	if rec.Level != slog.LevelWarn {
		t.Errorf("exit drop level = %v, want %v (a wedged conversation is degraded operation, not a lost delta)",
			rec.Level, slog.LevelWarn)
	}

	attrs := map[string]string{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if got := attrs["event"]; got != "stream_turn.exit_sink_full" {
		t.Errorf("exit drop event = %q, want %q", got, "stream_turn.exit_sink_full")
	}
	if got := attrs["session_id"]; got != "sess-a" {
		t.Errorf("exit drop session_id = %q, want %q", got, "sess-a")
	}
	if _, ok := attrs["kind"]; ok {
		t.Errorf("exit drop carries a %q attr; there is no event to name", "kind")
	}
	if len(attrs) != 2 {
		t.Errorf("exit drop attrs = %v, want exactly event + session_id", attrs)
	}
}

// Tool events fan through the unchanged emitter: a tool_use assistant block and a
// tool_result user line map to tool_use + tool_result envelopes (a light check of
// the #1088 parser + #627 mapper reuse, not a re-test of the mapper).
func TestStreamTurnDrainV2_ToolEvents(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set("sess-a")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil, discardLogger())
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	feedLines(sink, "sess-a", toolUseLine, toolRsltLine, resultLine)

	got := collectEnvs(t, bcast.pushed, 5)
	wantTypes := []string{
		protocol.TypeTurnState,  // responding
		protocol.TypeToolUse,    // Read
		protocol.TypeToolResult, // ok
		protocol.TypeTurnEnd,    // end_turn
		protocol.TypeTurnState,  // idle
	}
	if !slices.Equal(envTypes(got), wantTypes) {
		t.Fatalf("tool envelope types:\n got %v\nwant %v", envTypes(got), wantTypes)
	}
}

// --- #1496: the closing-class reserve ----------------------------------------

// saturatingBuf is the fixture buffer for the reserve tests: small enough to
// saturate by hand and timing-free, large enough that the reserve arithmetic
// leaves BOTH a non-empty droppable band and a non-empty reserve (droppableCap 4,
// reserve 4). A buffer of 1 degenerates to reserve 0 and would pin nothing.
const saturatingBuf = 8

// startBusyDrainFor starts a drain that feeds tr and pushes nothing: the active
// session is never set, so every event drops at the drain's gate AFTER observe.
// The tracker is the only thing under test, so the emitter is a real one wired to
// a bcast nothing reads.
func startBusyDrainFor(t *testing.T, ctx context.Context, sink *streamTurnSink, tr *turnBusyTracker) {
	t.Helper()
	cur := &stubCursor{}
	cur.set(testConvID)
	emitter := newInteractiveTurnEmitterV2(cur, newChanBcast("conn-a"), discardLogger())
	active := &stubActiveSession{} // never set → ok=false, so nothing reaches Push
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, tr, discardLogger())
	t.Cleanup(cleanup) // the caller's deferred cancel runs first: cancel-then-join
}

// awaitObserves blocks until n resolve calls have been seen, which is the drain's
// own goroutine reporting progress through the FIFO. Since observe resolves BEFORE
// it marks and the drain is serial, seeing call n proves call n-1's mark landed.
func awaitObserves(t *testing.T, resolved <-chan struct{}, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-resolved:
		case <-time.After(2 * time.Second):
			t.Fatalf("drain reached the tracker %d/%d times", i, n)
		}
	}
}

// #1496: the reserve arithmetic. The reserve PARTITIONS the buffer and adds none
// of it, which is what keeps the fan-in strictly bounded against a runaway child —
// so cap(ch) is asserted alongside droppableCap on every row.
//
// Both bounds of the min() are pinned: the constant binds at the production 256,
// the buf/2 clamp binds below 2*streamTurnSinkCloseReserve, and the degenerate
// buffer of 1 yields a reserve of 0 rather than starving the droppable class.
func TestNewStreamTurnSink_ReserveArithmetic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		buf          int
		wantCap      int
		wantDroppabl int
	}{
		{"default fills in the production buffer", 0, streamTurnSinkBuf, 224},
		{"production buffer: the constant binds", streamTurnSinkBuf, 256, 224},
		{"twice the reserve: the two bounds meet", 2 * streamTurnSinkCloseReserve, 64, 32},
		{"below that: the buf/2 clamp binds", saturatingBuf, 8, 4},
		{"small even buffer", 4, 4, 2},
		{"degenerate single slot: reserve 0, every slot droppable", 1, 1, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStreamTurnSink(tc.buf, discardLogger())
			if got := cap(s.ch); got != tc.wantCap {
				t.Errorf("cap(ch) = %d, want %d (the reserve partitions capacity, it never adds any)", got, tc.wantCap)
			}
			if s.droppableCap != tc.wantDroppabl {
				t.Errorf("droppableCap = %d, want %d", s.droppableCap, tc.wantDroppabl)
			}
			if s.droppableCap < 1 || s.droppableCap > cap(s.ch) {
				t.Errorf("droppableCap = %d out of range [1, %d]", s.droppableCap, cap(s.ch))
			}
		})
	}
}

// AC1: a turn-closing event survives a saturated fan-in. saturatingBuf openers are
// pushed before any drain exists — on the pre-#1496 policy every one of them is
// admitted, the channel is full, and the TurnEnd that follows is dropped, leaving
// the conversation busy with nothing left that could ever clear it. The reserve is
// what keeps the tail slots free for the closer.
//
// The fill is done with the drain STOPPED, so it is deterministic and timing-free;
// the drain starts afterwards and consumes what is already queued.
func TestStreamTurnSink_TurnEndSurvivesSaturatedFanIn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newStreamTurnSink(saturatingBuf, discardLogger())
	send := sink.sinkFor("sess-a")
	for range saturatingBuf {
		send(turnevent.TextChunk{MessageID: "m1", Text: "burst"})
	}
	send(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	resolved := make(chan struct{}, saturatingBuf+2)
	tr := newTurnBusyTracker(func(sid string) (string, bool) {
		select {
		case resolved <- struct{}{}:
		default:
		}
		return testConvID, sid == "sess-a"
	}, discardLogger())

	startBusyDrainFor(t, ctx, sink, tr)

	// Barrier before the wait: the SECOND observe proves the FIRST opener's mark
	// already landed, so the conversation is busy by the time WaitIdle is called.
	// Without it WaitIdle could return nil having never seen an open turn at all,
	// which would pass on the unmodified tree too.
	awaitObserves(t, resolved, 2)

	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	if err := tr.WaitIdle(waitCtx, testConvID); err != nil {
		t.Fatalf("conversation still busy after the drain emptied the fan-in: %v "+
			"(a TurnEnd crowded out of the fan-in can never be re-sent)", err)
	}
}

// AC2: the close stays ordered behind what was already queued. The tracker's
// resolve records Busy at the instant it is called, and observe resolves BEFORE it
// marks on a serial drain — so the recorded slice IS the processing order.
//
// Exactly one false reading, and it is the first: every envelope after the opening
// one, the TurnEnd included, sees a conversation already reported busy. That is
// "never idle while an earlier envelope is unprocessed" as a deterministic
// sequence rather than a timing window.
func TestStreamTurnSink_CloseStaysOrderedBehindQueued(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newStreamTurnSink(saturatingBuf, discardLogger())
	send := sink.sinkFor("sess-a")
	for range saturatingBuf {
		send(turnevent.TextChunk{MessageID: "m1", Text: "burst"})
	}
	send(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	var (
		tr   *turnBusyTracker
		mu   sync.Mutex
		seen []bool
	)
	resolved := make(chan struct{}, saturatingBuf+2)
	// tr is assigned before the drain goroutine is spawned, so the closure's read
	// of it is ordered by that go statement.
	tr = newTurnBusyTracker(func(sid string) (string, bool) {
		// Safe from inside resolve: observe calls it OUTSIDE t.mu, deliberately.
		busy := tr.Busy(testConvID)
		mu.Lock()
		seen = append(seen, busy)
		mu.Unlock()
		select {
		case resolved <- struct{}{}:
		default:
		}
		return testConvID, sid == "sess-a"
	}, discardLogger())

	startBusyDrainFor(t, ctx, sink, tr)

	awaitObserves(t, resolved, 2)
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	if err := tr.WaitIdle(waitCtx, testConvID); err != nil {
		t.Fatalf("conversation still busy after the drain emptied the fan-in: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("recorded %d observes, want at least the opener and the closer", len(seen))
	}
	if seen[0] {
		t.Errorf("first observed reading = busy, want idle (nothing had opened the turn yet)")
	}
	for i, busy := range seen[1:] {
		if !busy {
			t.Errorf("observe %d read idle while envelope %d was still unprocessed; readings = %v",
				i+1, i+1, seen)
		}
	}
}

// AC3: the producer is never wedged. With the drain never started, every send past
// capacity must still return — a blocking send here would stall claude's stdout
// forwarder, which is the failure sinkFor exists to prevent. Both closing-class
// lanes are driven (TurnEnd through sinkFor, the exit envelope through exitFor)
// because both bypass the droppable watermark.
func TestStreamTurnSink_ProducerNeverBlocksWithoutDrain(t *testing.T) {
	t.Parallel()

	sink := newStreamTurnSink(saturatingBuf, discardLogger())
	send := sink.sinkFor("sess-a")
	exit := sink.exitFor("sess-a")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range saturatingBuf * 4 {
			send(turnevent.TextChunk{MessageID: "m1", Text: "burst"})
			send(turnevent.ToolStart{ToolCallID: "tu-1", Title: "Read"})
			send(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
			exit()
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a sink send blocked on a full fan-in with the drain stopped")
	}
}

// AC4: delta-class loss is unchanged — still dropped-newest, still Debug, still
// exactly event + kind + session_id, still carrying none of the event's content.
// Green on both sides of #1496; it is the pin that the reserve narrowed WHEN a
// droppable is dropped without touching WHAT the drop reports.
func TestStreamTurnSink_DroppableDropUnchanged(t *testing.T) {
	t.Parallel()

	const secret = "SECRET-ASSISTANT-TEXT"
	recs := make(chan slog.Record, 32)
	sink := newStreamTurnSink(saturatingBuf, slog.New(dropWatcher{recs: recs}))
	send := sink.sinkFor("sess-a")

	// No drain: one send past the whole buffer drops on either policy.
	for range saturatingBuf + 1 {
		send(turnevent.TextChunk{MessageID: "m1", Text: secret})
	}

	rec := waitRecord(t, recs, "stream_turn.sink_full")
	if rec.Level != slog.LevelDebug {
		t.Errorf("droppable drop level = %v, want %v (a lost delta is not degraded operation)",
			rec.Level, slog.LevelDebug)
	}
	attrs := map[string]string{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if got := attrs["kind"]; got != "text_chunk" {
		t.Errorf("droppable drop kind = %q, want %q", got, "text_chunk")
	}
	if got := attrs["session_id"]; got != "sess-a" {
		t.Errorf("droppable drop session_id = %q, want %q", got, "sess-a")
	}
	if len(attrs) != 3 {
		t.Errorf("droppable drop attrs = %v, want exactly event + kind + session_id", attrs)
	}
	// Content-free: the fed text reaches no attribute and no message.
	for k, v := range attrs {
		if strings.Contains(v, secret) {
			t.Errorf("droppable drop attr %q carries event content: %q", k, v)
		}
	}
	if strings.Contains(rec.Message, secret) {
		t.Errorf("droppable drop message carries event content: %q", rec.Message)
	}
}

// AC5: a lost turn-closer is never silent. Loss is narrowed rather than made
// impossible — past the reserve a TurnEnd can still be crowded out — so the
// remaining path must be visible at the daemon's default LevelInfo, exactly as
// TestStreamTurnSink_ExitDropWhenFull requires of the exit lane.
//
// The record carries "kind" where the exit drop does not, and that is the same
// content-free discipline rather than a departure from it: discriminant and
// session id only, and here there IS an event to name.
//
// No drain is started: with no reader the buffer-of-1 fill is deterministic and
// timing-free. The watcher goes on the SINK's logger — the drop is emitted from
// the sink closure, never from the drain.
func TestStreamTurnSink_TurnEndDropWhenFull(t *testing.T) {
	t.Parallel()

	recs := make(chan slog.Record, 8)
	// newStreamTurnSink only replaces buf when it is <= 0, so 1 survives; at 1 the
	// reserve degenerates to 0, which is what makes the single slot occupiable by a
	// droppable and the closer's drop reachable.
	sink := newStreamTurnSink(1, slog.New(dropWatcher{recs: recs}))
	send := sink.sinkFor("sess-a")

	send(turnevent.TextChunk{MessageID: "m1", Text: "occupies the slot"}) // silently
	send(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})       // no room: dropped and logged

	var rec slog.Record
	select {
	case rec = <-recs:
	default:
		t.Fatal("no record logged for a TurnEnd dropped on a full sink")
	}

	if rec.Level != slog.LevelWarn {
		t.Errorf("turn-close drop level = %v, want %v (a wedged conversation is degraded operation, not a lost delta)",
			rec.Level, slog.LevelWarn)
	}

	attrs := map[string]string{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if got := attrs["event"]; got != "stream_turn.close_sink_full" {
		t.Errorf("turn-close drop event = %q, want %q", got, "stream_turn.close_sink_full")
	}
	if got := attrs["kind"]; got != "turn_end" {
		t.Errorf("turn-close drop kind = %q, want %q", got, "turn_end")
	}
	if got := attrs["session_id"]; got != "sess-a" {
		t.Errorf("turn-close drop session_id = %q, want %q", got, "sess-a")
	}
	if len(attrs) != 3 {
		t.Errorf("turn-close drop attrs = %v, want exactly event + kind + session_id", attrs)
	}
}

// --- #1133: the live session tag -------------------------------------------

// TestStreamSessionTag_RotatesAndRefusesEmpty covers the tag's whole contract in
// one sequence, because the sequence IS the contract: a refused rotation must leave
// the previous value rather than reset to the construction one, which a per-case
// table with a fresh tag each row cannot distinguish.
//
// The empty-id refusal is the invariant this type owns — an empty tag matches no
// bound session (boundSessionIDForActive reports ok == false for an empty
// CurrentSessionID, and no non-empty active can equal ""), so it would black-hole
// the conversation's stream for the life of the runner. RestartFresh refuses ""
// above its own fire site, so production never reaches this guard; it is here
// because the invariant belongs to the value, not to one of its callers.
func TestStreamSessionTag_RotatesAndRefusesEmpty(t *testing.T) {
	t.Parallel()
	tag := newStreamSessionTag("sess-a")
	if got := tag.ID(); got != "sess-a" {
		t.Fatalf("fresh tag ID() = %q, want %q", got, "sess-a")
	}
	tag.Rotate("sess-b")
	if got := tag.ID(); got != "sess-b" {
		t.Fatalf("after Rotate(%q) ID() = %q, want %q", "sess-b", got, "sess-b")
	}
	tag.Rotate("")
	if got := tag.ID(); got != "sess-b" {
		t.Fatalf("after the refused Rotate(\"\") ID() = %q, want the previous %q held", got, "sess-b")
	}
	tag.Rotate("sess-c")
	if got := tag.ID(); got != "sess-c" {
		t.Fatalf("after Rotate(%q) ID() = %q, want %q — the refusal left the tag unusable", "sess-c", got, "sess-c")
	}
}

// TestStreamTurnSink_TagRotationRetagsBothLanes is AC1 at the fan-in tier: after a
// rotation, BOTH lanes newStreamRunnerFactory installs on a runner — the turnevent
// sink and the child-exit callback — tag their envelopes with the new id.
//
// Both lanes in ONE test, against ONE tag, is the assertion rather than a
// convenience: identical tags on the two lanes are what let the drain's exit arm
// clear exactly the conversation whose events it is ordered behind, so a rotation
// that moved one lane and not the other is the defect worth catching, and two
// separate tests could each pass while that held.
//
// No drain runs — the envelopes are read straight off the channel, so the ordering
// is the send ordering and nothing is timing-dependent.
func TestStreamTurnSink_TagRotationRetagsBothLanes(t *testing.T) {
	t.Parallel()
	sink := newStreamTurnSink(0, discardLogger())
	tag := newStreamSessionTag("sess-old")
	events := sink.sinkForTag(tag.ID)
	exit := sink.exitForTag(tag.ID)

	events(turnevent.TextChunk{MessageID: "m1", Text: "before"})
	exit()
	tag.Rotate("sess-new")
	events(turnevent.TextChunk{MessageID: "m2", Text: "after"})
	exit()

	want := []streamTurnEnvelope{
		{sessionID: "sess-old"},
		{sessionID: "sess-old", exit: true},
		{sessionID: "sess-new"},
		{sessionID: "sess-new", exit: true},
	}
	for i, w := range want {
		var got streamTurnEnvelope
		select {
		case got = <-sink.ch:
		default:
			t.Fatalf("envelope %d never arrived; want session_id=%q exit=%t", i, w.sessionID, w.exit)
		}
		if got.sessionID != w.sessionID || got.exit != w.exit {
			t.Errorf("envelope %d: session_id=%q exit=%t, want session_id=%q exit=%t",
				i, got.sessionID, got.exit, w.sessionID, w.exit)
		}
	}
}

// #1483 at the fan-in tier: the exit lane is stamped with its own position, in push
// order, and the EVENT lane is not stamped at all.
//
// Both halves are the assertion. A counter that also advanced on events would still
// give the guard a monotone position, so the positive half alone cannot catch it —
// but it would put an unsynchronised increment on claude's stdout forwarder for
// every event, which is the cost the exits-only choice exists to avoid.
//
// No drain runs: the envelopes are read straight off the channel, so the ordering is
// the send ordering and nothing is timing-dependent — the shape
// TestStreamTurnSink_TagRotationRetagsBothLanes established.
func TestStreamTurnSink_ExitEpochStampsExitsOnly(t *testing.T) {
	t.Parallel()
	sink := newStreamTurnSink(0, discardLogger())

	if got := sink.exitEpoch(); got != 0 {
		t.Fatalf("exitEpoch() on a fresh sink = %d, want 0", got)
	}

	events := sink.sinkFor("sess-a")
	exit := sink.exitFor("sess-a")

	events(turnevent.TextChunk{MessageID: "m1", Text: "before"})
	exit()
	events(turnevent.TextChunk{MessageID: "m2", Text: "after"})
	exit()

	want := []struct {
		exit      bool
		exitEpoch uint64
	}{
		{exit: false, exitEpoch: 0},
		{exit: true, exitEpoch: 1},
		{exit: false, exitEpoch: 0},
		{exit: true, exitEpoch: 2},
	}
	for i, w := range want {
		var got streamTurnEnvelope
		select {
		case got = <-sink.ch:
		default:
			t.Fatalf("envelope %d never arrived; want exit=%t exit_epoch=%d", i, w.exit, w.exitEpoch)
		}
		if got.exit != w.exit || got.exitEpoch != w.exitEpoch {
			t.Errorf("envelope %d: exit=%t exit_epoch=%d, want exit=%t exit_epoch=%d",
				i, got.exit, got.exitEpoch, w.exit, w.exitEpoch)
		}
	}

	if got := sink.exitEpoch(); got != 2 {
		t.Errorf("exitEpoch() after two exits and two events = %d, want 2 — only exits advance the lane", got)
	}
}

// TestStreamTurnSink_TagRotationRefusedLeavesEnvelopeTag is AC3 carried through to
// what actually rides on it: a refused rotation must leave the ENVELOPES tagged as
// they were, not merely the tag's field. An empty tag would match no bound session,
// so every later envelope would be dropped at the drain's gate and that
// conversation would be dark for the life of the runner — the failure mode is
// silent, which is why it is asserted at the envelope rather than at the accessor.
func TestStreamTurnSink_TagRotationRefusedLeavesEnvelopeTag(t *testing.T) {
	t.Parallel()
	sink := newStreamTurnSink(0, discardLogger())
	tag := newStreamSessionTag("sess-old")
	events := sink.sinkForTag(tag.ID)

	tag.Rotate("")
	events(turnevent.TextChunk{MessageID: "m1", Text: "after the refusal"})

	select {
	case got := <-sink.ch:
		if got.sessionID != "sess-old" {
			t.Errorf("envelope session_id = %q after the refused rotation, want %q held", got.sessionID, "sess-old")
		}
	default:
		t.Fatal("no envelope arrived")
	}
}

// TestStreamTurnDrainV2_PostRotationForwardsAndStaleStillDrops is the pair of ACs
// that have to hold TOGETHER, which is why they are one test: after a rotation the
// drain forwards the rotated runner's events for the conversation now bound to the
// new id (AC1 — the dark-stream defect is gone), AND an envelope still carrying the
// pre-rotation id is still dropped before emitter.Handle with the unchanged
// content-free record (AC2 — the gate stays fail-closed and was not widened).
//
// Asserting only the first would pass against a gate someone had relaxed to a
// fallback or a prefix match, which is the one change this ticket must not make.
//
// The stale envelope is fed LAST and its drop is the barrier: the drain is serial,
// so observing it proves the forwarded event ahead of it was already handled.
func TestStreamTurnDrainV2_PostRotationForwardsAndStaleStillDrops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set("sess-old")
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	drops := make(chan string, 8)
	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil,
		slog.New(dropWatcher{kinds: drops}))
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	// The rotation, in the production order: the pool rebinds the conversation to
	// the new id first (startFreshRunner's rotate), then RestartFresh moves the tag.
	tag := newStreamSessionTag("sess-old")
	active.set("sess-new")
	tag.Rotate("sess-new")

	feedLinesTagged(sink, tag, assistantTextLine("m2", "post-rotation"), resultLine)

	got := collectEnvs(t, bcast.pushed, 4)
	wantTypes := []string{
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // post-rotation
		protocol.TypeTurnEnd,        // end_turn
		protocol.TypeTurnState,      // idle
	}
	if !slices.Equal(envTypes(got), wantTypes) {
		t.Fatalf("post-rotation envelope types:\n got %v\nwant %v", envTypes(got), wantTypes)
	}
	d := decodeDelta(t, got[1])
	if d.Text != "post-rotation" {
		t.Errorf("assistant_delta text = %q, want %q", d.Text, "post-rotation")
	}
	if d.ConversationID != testConvID {
		t.Errorf("assistant_delta conversation_id = %q, want %q", d.ConversationID, testConvID)
	}

	// A producer that has NOT rotated onto the active conversation's bound session
	// is still refused, and the record is still the content-free not-active one.
	feedLines(sink, "sess-old", assistantTextLine("m3", "STALE"), resultLine)
	waitDropKind(t, drops, "turn_end")
	assertNoPush(t, bcast.pushed)
}

// TestStreamSessionTag_ConcurrentRotateAndRead exists for the -race run: the tag is
// written from the dispatch goroutine running RestartFresh and read per event on
// claude's stdout forwarder goroutine, so an unsynchronised field would be a real
// data race rather than a theoretical one. The value assertion is deliberately weak
// — every read must return one of the ids ever stored, never "" — because which id
// a given read sees is exactly the thing that is racing.
func TestStreamSessionTag_ConcurrentRotateAndRead(t *testing.T) {
	t.Parallel()
	const rounds = 200
	tag := newStreamSessionTag("sess-0")
	valid := map[string]bool{"sess-0": true}
	for i := 1; i <= rounds; i++ {
		valid[fmt.Sprintf("sess-%d", i)] = true
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= rounds; i++ {
			tag.Rotate(fmt.Sprintf("sess-%d", i))
		}
	}()
	bad := make(chan string, 1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if got := tag.ID(); !valid[got] {
				select {
				case bad <- got:
				default:
				}
			}
		}
	}()
	wg.Wait()
	select {
	case got := <-bad:
		t.Fatalf("concurrent read saw %q, which was never stored", got)
	default:
	}
}
