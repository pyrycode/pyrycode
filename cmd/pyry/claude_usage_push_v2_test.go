package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	usageLineBoth = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1790367600,"rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"utilization":0.44,"resetsAt":1790367600},"seven_day":{"utilization":0.64,"resetsAt":1790575200}}}}`
	usageLineNone = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1790367600,"rateLimitType":"five_hour"}}`
	usageLineOne  = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1790367600,"rateLimitType":"five_hour","unifiedWindows":{"seven_day":{"utilization":0.65,"resetsAt":1790575200}}}}`
)

// usagePushBcast is a goroutine-safe interactiveBroadcaster: Run pushes from its
// own goroutine while the test reads. Every Push attempt, failed ones included,
// is delivered on pushes, and every ActiveConns snapshot signals snapshots.
type usagePushBcast struct {
	mu        sync.Mutex
	conns     []relay.ActiveConn
	pushErr   map[string]error
	pushes    chan recordedPush
	snapshots chan struct{}
}

func newUsagePushBcast(conns ...relay.ActiveConn) *usagePushBcast {
	return &usagePushBcast{conns: conns, pushes: make(chan recordedPush, 64), snapshots: make(chan struct{}, 64)}
}

func (b *usagePushBcast) setConns(conns ...relay.ActiveConn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.conns = conns
}

func (b *usagePushBcast) ActiveConns(context.Context) []relay.ActiveConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snapshots <- struct{}{}
	return append([]relay.ActiveConn(nil), b.conns...)
}

func (b *usagePushBcast) Push(_ context.Context, connID string, env protocol.Envelope) error {
	b.mu.Lock()
	err := b.pushErr[connID]
	b.mu.Unlock()
	b.pushes <- recordedPush{connID: connID, env: env}
	return err
}

// next returns the next push attempt, failing the test if none arrives.
func (b *usagePushBcast) next(t *testing.T) recordedPush {
	t.Helper()
	select {
	case p := <-b.pushes:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no push arrived")
		return recordedPush{}
	}
}

// none fails the test if a push is already waiting.
func (b *usagePushBcast) none(t *testing.T) {
	t.Helper()
	select {
	case p := <-b.pushes:
		t.Fatalf("unexpected push to %s: %s", p.connID, p.env.Payload)
	default:
	}
}

// usagePushRig wires a real parser into a real store and starts the push stream
// over bcast, as startRelayV2 does.
type usagePushRig struct {
	store   *claudeUsageStore
	parser  *streamsup.Parser
	cleanup func()
}

func newUsagePushRig(t *testing.T, bcast interactiveBroadcaster) *usagePushRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store := &claudeUsageStore{}
	parser := streamsup.NewParser(func(turnevent.Event) {}, quietLogger())
	parser.SetUsageWindowsHandler(store.record)
	stop := startClaudeUsageStreamV2(ctx, store, bcast, quietLogger())
	r := &usagePushRig{store: store, parser: parser, cleanup: func() { cancel(); stop() }}
	t.Cleanup(r.cleanup)
	return r
}

func (r *usagePushRig) write(t *testing.T, line string) {
	t.Helper()
	if _, err := r.parser.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
}

// assertUsagePush checks p is an uncorrelated claude_usage frame carrying want.
func assertUsagePush(t *testing.T, p recordedPush, wantConn, want string) {
	t.Helper()
	if p.connID != wantConn {
		t.Errorf("push went to %q, want %q", p.connID, wantConn)
	}
	if p.env.Type != protocol.TypeClaudeUsage {
		t.Errorf("push type %q, want %q", p.env.Type, protocol.TypeClaudeUsage)
	}
	if p.env.InReplyTo != nil {
		t.Errorf("push carries in_reply_to %d; nothing solicited it", *p.env.InReplyTo)
	}
	if p.env.EventID != nil {
		t.Errorf("push carries event_id %d; it must stay out of the replay ring", *p.env.EventID)
	}
	if got := string(p.env.Payload); got != want {
		t.Errorf("push payload %s, want %s", got, want)
	}
}

// pushesByConn reads n pushes and groups their payloads by connection, since
// ActiveConns is an unordered set.
func pushesByConn(t *testing.T, b *usagePushBcast, n int) map[string][]recordedPush {
	t.Helper()
	out := map[string][]recordedPush{}
	for range n {
		p := b.next(t)
		out[p.connID] = append(out[p.connID], p)
	}
	return out
}

// Two open connections, an interactive one and one without the capability, each
// receive every new reading once, in the order the store took them, carrying the
// bytes request_claude_usage answers for that reading.
func TestClaudeUsagePush_ReachesEveryConnInOrder(t *testing.T) {
	t.Parallel()
	bcast := newUsagePushBcast(
		relay.ActiveConn{ConnID: "phone", Interactive: true},
		relay.ActiveConn{ConnID: "watch"},
	)
	rig := newUsagePushRig(t, bcast)

	rig.write(t, usageLineBoth)
	first := requestClaudeUsageWire(t, rig.store)
	rig.write(t, usageLineOne)
	second := requestClaudeUsageWire(t, rig.store)
	if first == second {
		t.Fatalf("the two readings answer the same bytes %s", first)
	}

	got := pushesByConn(t, bcast, 4)
	for _, conn := range []string{"phone", "watch"} {
		if len(got[conn]) != 2 {
			t.Fatalf("%s received %d pushes, want 2", conn, len(got[conn]))
		}
		assertUsagePush(t, got[conn][0], conn, first)
		assertUsagePush(t, got[conn][1], conn, second)
	}
	bcast.none(t)
}

// A rate_limit_event line without unifiedWindows leaves the reading as it was
// and pushes nothing: the next push is the next stored reading.
func TestClaudeUsagePush_LineWithoutWindowsPushesNothing(t *testing.T) {
	t.Parallel()
	bcast := newUsagePushBcast(relay.ActiveConn{ConnID: "phone", Interactive: true})
	rig := newUsagePushRig(t, bcast)

	rig.write(t, usageLineBoth)
	assertUsagePush(t, bcast.next(t), "phone", requestClaudeUsageWire(t, rig.store))

	rig.write(t, usageLineNone)
	rig.write(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":null}}`)
	rig.write(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{}}}`)
	rig.write(t, usageLineOne)
	assertUsagePush(t, bcast.next(t), "phone", requestClaudeUsageWire(t, rig.store))
	bcast.none(t)
}

// A connection whose push fails does not stop the fan-out to the others.
func TestClaudeUsagePush_FailedConnDoesNotStopTheFanOut(t *testing.T) {
	t.Parallel()
	bcast := newUsagePushBcast(
		relay.ActiveConn{ConnID: "gone", Interactive: true},
		relay.ActiveConn{ConnID: "live", Interactive: true},
	)
	bcast.pushErr = map[string]error{"gone": relay.ErrConnNotFound}
	rig := newUsagePushRig(t, bcast)

	rig.write(t, usageLineBoth)
	got := pushesByConn(t, bcast, 2)
	if len(got["live"]) != 1 {
		t.Fatalf("live received %d pushes, want 1 despite the failing conn", len(got["live"]))
	}
	assertUsagePush(t, got["live"][0], "live", requestClaudeUsageWire(t, rig.store))
}

// With no connection open, storing a reading raises nothing and pushes nothing.
// A connection that opens later is not sent the earlier reading; it gets it by
// asking, and receives the next reading as a push.
func TestClaudeUsagePush_NoConnOpen(t *testing.T) {
	t.Parallel()
	bcast := newUsagePushBcast()
	rig := newUsagePushRig(t, bcast)

	rig.write(t, usageLineBoth)
	want := `{"state":"observed","observed_at":"`
	if got := requestClaudeUsageWire(t, rig.store); !strings.HasPrefix(got, want) {
		t.Fatalf("reply after a reading with no conn open: %s", got)
	}
	// The fan-out snapshots asynchronously; open the late conn only after the
	// first reading's snapshot found none.
	select {
	case <-bcast.snapshots:
	case <-time.After(5 * time.Second):
		t.Fatal("the first reading was never fanned out")
	}

	bcast.setConns(relay.ActiveConn{ConnID: "late", Interactive: true})
	rig.write(t, usageLineOne)
	assertUsagePush(t, bcast.next(t), "late", requestClaudeUsageWire(t, rig.store))
	bcast.none(t)
}

// After the relay leg's cleanup the store still records but pushes nothing.
func TestClaudeUsagePush_AfterCleanup(t *testing.T) {
	t.Parallel()
	bcast := newUsagePushBcast(relay.ActiveConn{ConnID: "phone", Interactive: true})
	rig := newUsagePushRig(t, bcast)
	rig.cleanup()

	rig.write(t, usageLineBoth)
	if got := requestClaudeUsageWire(t, rig.store); !strings.Contains(got, `"five_hour":{"utilization":0.44`) {
		t.Fatalf("store did not record after cleanup: %s", got)
	}
	bcast.none(t)
}

// notify never blocks: with the buffer full and nothing draining it, a reading
// is dropped with a content-free Debug line. It runs under the store's lock on a
// Claude session's stdout forwarder, so blocking would stall every session.
func TestClaudeUsageEmitter_NotifyNeverBlocks(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	e := newClaudeUsageEmitterV2(debugLogger(&buf))
	store := &claudeUsageStore{}
	store.setOnRecord(e.notify)

	done := make(chan struct{})
	go func() {
		defer close(done)
		u := 0.5
		for range claudeUsageQueueSize + 3 {
			store.record(&streamsup.UsageWindow{Utilization: &u}, nil)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("record blocked on a full push queue")
	}
	if n := strings.Count(buf.String(), "claude_usage.queue_full"); n != 3 {
		t.Errorf("logged %d queue_full drops, want 3; got:\n%s", n, buf.String())
	}
	if strings.Contains(buf.String(), "0.5") {
		t.Errorf("a drop line carries the reading; got:\n%s", buf.String())
	}
}
