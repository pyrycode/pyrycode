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

func waitDropKind(t *testing.T, kinds <-chan string, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case k := <-kinds:
			if k == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a not-active drop of kind %q", want)
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
