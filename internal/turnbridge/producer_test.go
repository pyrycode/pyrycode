package turnbridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// collector records OnEvent invocations from the (single) Run goroutine.
type collector struct {
	mu     sync.Mutex
	events []turnevent.Event
}

func (c *collector) onEvent(e turnevent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) snapshot() []turnevent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]turnevent.Event(nil), c.events...)
}

func TestNew_RequiresSubscribe(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with nil Subscribe: got nil error, want non-nil")
	}
	if _, err := New(Config{Subscribe: func(context.Context) (<-chan tuidriver.Event, error) { return nil, nil }}); err != nil {
		t.Fatalf("New with valid Subscribe: %v", err)
	}
}

func TestDrain_OnChannelClose(t *testing.T) {
	t.Parallel()

	ch := make(chan tuidriver.Event, 3)
	ch <- jsonlEvent(entry(t, "assistant", "m1", "", map[string]any{"type": "text", "text": "hello"}))
	ch <- kindEvent(tuidriver.EventKindPtyIdle) // dropped, not forwarded
	ch <- kindEvent(tuidriver.EventKindJsonlEndOfTurn)
	close(ch)

	c := &collector{}
	p := &Producer{onEvent: c.onEvent, log: testLogger()}
	p.drain(context.Background(), ch)

	want := []turnevent.Event{
		turnevent.TextChunk{MessageID: "m1", Text: "hello"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	}
	if got := c.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("drained events:\n got %#v\nwant %#v", got, want)
	}
}

func TestDrain_OnCtxCancel(t *testing.T) {
	t.Parallel()

	ch := make(chan tuidriver.Event) // never fed
	c := &collector{}
	p := &Producer{onEvent: c.onEvent, log: testLogger()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.drain(ctx, ch)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return after ctx cancel")
	}
}

// A fire on FlushSignal invokes OnFlush on the single Run goroutine — the #609
// seam that lets a consumer flush coalescing state mutated across OnEvent calls
// without a lock. The blocking send + the flushed channel give a deterministic
// happens-before, so the assertion is race-clean.
func TestDrain_FlushSignalInvokesOnFlush(t *testing.T) {
	t.Parallel()

	ch := make(chan tuidriver.Event) // never fed — only the flush arm fires
	flush := make(chan time.Time)
	flushed := make(chan struct{}, 1)
	p := &Producer{
		onEvent:     func(turnevent.Event) {},
		flushSignal: flush,
		onFlush:     func() { flushed <- struct{}{} },
		log:         testLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.drain(ctx, ch); close(done) }()

	flush <- time.Time{} // fire the flush arm
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("OnFlush not invoked after FlushSignal fired")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return after ctx cancel")
	}
}

func TestDrain_NilOnEventIsNoop(t *testing.T) {
	t.Parallel()

	ch := make(chan tuidriver.Event, 2)
	ch <- jsonlEvent(entry(t, "assistant", "m1", "", map[string]any{"type": "text", "text": "hello"}))
	ch <- kindEvent(tuidriver.EventKindStallDetected)
	close(ch)

	p := &Producer{onEvent: nil, log: testLogger()}
	p.drain(context.Background(), ch) // must drain and return without panicking
}

// TestRun_ResubscribesAcrossRestart is the "no leaked goroutine across a
// restart" observable: when the first stream closes (session restart), Run
// subscribes again and drains the next stream. The fake Subscriber hands out
// ch1, then ch2, then blocks on ctx and returns ctx.Err().
func TestRun_ResubscribesAcrossRestart(t *testing.T) {
	t.Parallel()

	ch1 := make(chan tuidriver.Event)
	ch2 := make(chan tuidriver.Event)
	streams := []chan tuidriver.Event{ch1, ch2}

	var mu sync.Mutex
	calls := 0
	subscribe := func(ctx context.Context) (<-chan tuidriver.Event, error) {
		mu.Lock()
		n := calls
		calls++
		mu.Unlock()
		if n < len(streams) {
			return streams[n], nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	c := &collector{}
	p, err := New(Config{Subscribe: subscribe, OnEvent: c.onEvent, Logger: testLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()

	// First stream: send is unbuffered, so it blocks until drain receives —
	// a natural sync point proving Run is draining ch1.
	ch1 <- jsonlEvent(entry(t, "assistant", "a", "", map[string]any{"type": "text", "text": "one"}))
	close(ch1) // session restart

	// Second stream: blocking send proves Run re-subscribed and is draining ch2.
	ch2 <- jsonlEvent(entry(t, "assistant", "b", "", map[string]any{"type": "text", "text": "two"}))
	close(ch2)

	cancel()
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	want := []turnevent.Event{
		turnevent.TextChunk{MessageID: "a", Text: "one"},
		turnevent.TextChunk{MessageID: "b", Text: "two"},
	}
	if got := c.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events across restart:\n got %#v\nwant %#v", got, want)
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls < 2 {
		t.Fatalf("Subscribe calls: got %d, want >= 2 (re-subscribe after restart)", gotCalls)
	}
}

// blockingHost is a SessionHost whose WaitForPTY blocks until its ctx is
// cancelled — modelling a stale/evicted session the producer must abandon when
// the operator switches conversations (#679). Session() is never reached.
type blockingHost struct{}

func (blockingHost) WaitForPTY(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (blockingHost) Session() *tuidriver.Session          { return nil }

func noResolve(context.Context) (string, int64, error) { return "", 0, nil }

// TestNewTargetSubscriber_SwitchAbortsWait is the AC#3 robustness gate: when the
// active conversation switches while the subscriber is blocked in WaitForPTY of a
// (stale) bound session, the switch must abort that wait and re-resolve onto the
// now-active target — the subscriber must NOT return / stop the producer. Without
// the switch-abortable wait the producer would wedge forever on the evicted
// session.
func TestNewTargetSubscriber_SwitchAbortsWait(t *testing.T) {
	t.Parallel()

	sw0 := make(chan struct{})
	sw1 := make(chan struct{}) // open, never fired — the 2nd target blocks in WaitForPTY
	switches := []<-chan struct{}{sw0, sw1}

	var mu sync.Mutex
	calls := 0
	resolve := func(ctx context.Context) (Target, error) {
		mu.Lock()
		n := calls
		calls++
		mu.Unlock()
		var sw <-chan struct{}
		if n < len(switches) {
			sw = switches[n]
		}
		return Target{Host: blockingHost{}, Resolve: noResolve, Switch: sw}, nil
	}
	resolveCalls := func() int { mu.Lock(); defer mu.Unlock(); return calls }

	sub := NewTargetSubscriber(resolve, tuidriver.NewTracker(tuidriver.TrackerOpts{}), testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := sub(ctx); done <- err }()

	// First subscription is parked in WaitForPTY(blockingHost). Fire the switch:
	// it must abandon the wait and re-resolve (calls reaches 2).
	close(sw0)
	deadline := time.After(2 * time.Second)
	for resolveCalls() < 2 {
		select {
		case err := <-done:
			t.Fatalf("subscriber returned %v instead of re-resolving on switch", err)
		case <-deadline:
			t.Fatalf("subscriber did not re-resolve after switch (resolve calls = %d)", resolveCalls())
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// It must still be running (blocked in the 2nd target's WaitForPTY), not done.
	select {
	case err := <-done:
		t.Fatalf("subscriber returned %v while parent ctx is still live", err)
	default:
	}

	// Parent cancel is the only thing that makes it return — with ctx.Err().
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("after parent cancel: got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber did not return after parent ctx cancel")
	}
}

// TestNewTargetSubscriber_ResolveErrorRetriesThenCtxCancel covers the
// unresolvable-bound path: a TargetResolver error backs off and retries (never
// returning a wrong target), and a parent-ctx cancel ends the retry loop with
// ctx.Err() — the only error the Subscriber contract permits.
func TestNewTargetSubscriber_ResolveErrorRetriesThenCtxCancel(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	calls := 0
	resolve := func(ctx context.Context) (Target, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return Target{}, errors.New("no bound session for active conversation")
	}

	sub := NewTargetSubscriber(resolve, tuidriver.NewTracker(tuidriver.TrackerOpts{}), testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := sub(ctx); done <- err }()

	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber did not return after ctx cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 1 {
		t.Fatalf("resolve calls = %d, want >= 1 (the resolver is consulted before backoff)", calls)
	}
}

func TestRun_ReturnsOnSubscribeCtxCancel(t *testing.T) {
	t.Parallel()

	subscribe := func(ctx context.Context) (<-chan tuidriver.Event, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p, err := New(Config{Subscribe: subscribe, Logger: testLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()

	cancel()
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
