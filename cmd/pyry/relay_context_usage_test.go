package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const ctxUsageTestConv = conversations.ConversationID("CONVERSATION_2431")

// fakeContextUsageQuerier counts round trips and reports what it was asked for. The
// detail it records is the assertion that the daemon — not the client — chooses it.
type fakeContextUsageQuerier struct {
	calls   atomic.Int64
	details chan string

	// block, when non-nil, holds each call until closed. It is how a test keeps a
	// flight in flight while a second ask arrives, with no sleep anywhere.
	block chan struct{}

	usage turnevent.ContextUsage
	ok    bool

	// ctxErr records whether the query's own context was already done when the
	// querier ran — the observable half of "the deadline is real".
	ctxErr atomic.Bool
}

func (f *fakeContextUsageQuerier) QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool) {
	f.calls.Add(1)
	select {
	case f.details <- detail:
	default:
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			f.ctxErr.Store(true)
			return turnevent.ContextUsage{}, false
		}
	}
	if ctx.Err() != nil {
		f.ctxErr.Store(true)
	}
	return f.usage, f.ok
}

func newFakeContextUsageQuerier(ok bool) *fakeContextUsageQuerier {
	return &fakeContextUsageQuerier{
		details: make(chan string, 8),
		ok:      ok,
		usage: turnevent.ContextUsage{
			Model:       "MODEL_2431",
			TotalTokens: 31337,
			MaxTokens:   200000,
			Percentage:  16,
		},
	}
}

// fakeClock is the injected time source. Advancing it is how the collapse window is
// crossed without sleeping on a wall clock, which is exactly what AC-2 asks for.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func ctxUsageResolverFor(t *testing.T, q *fakeContextUsageQuerier, busy *turnBusyTracker, clock *fakeClock) *contextUsageResolver {
	t.Helper()
	resolve := func(convID string) (contextUsageQuerier, conversations.ConversationID, bool) {
		if convID != string(ctxUsageTestConv) {
			return nil, "", false
		}
		return q, ctxUsageTestConv, true
	}
	return newContextUsageResolver(context.Background(), resolve, busy, clock.now)
}

// TestContextUsageResolver_AsksAtFullDetail pins that the daemon asks for the
// EXPENSIVE reading. The cheap "summary" is what the automatic post-turn path
// already publishes; a verb that asked for it too would cost a round trip to
// deliver what the client had moments ago.
func TestContextUsageResolver_AsksAtFullDetail(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
		t.Fatal("Get refused a resolvable conversation")
	}
	select {
	case got := <-q.details:
		if got != "full" {
			t.Errorf("asked claude for detail %q, want %q", got, "full")
		}
	default:
		t.Fatal("querier was never asked")
	}
}

// TestContextUsageResolver_CollapsesInFlight is AC-2's first side: a second ask
// arriving while the first is still in flight joins it rather than writing a second
// request to the child. No sleep — the second ask is released by the barrier the
// first is parked on.
func TestContextUsageResolver_CollapsesInFlight(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	q.block = make(chan struct{})
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	type result struct {
		payload protocol.ContextUsagePayload
		ok      bool
	}
	results := make(chan result, 2)
	// The first ask parks inside the querier; the second must find its flight.
	go func() {
		p, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		results <- result{p, ok}
	}()
	// Wait until the querier is actually running, so the second ask cannot win the
	// race to install the flight — the barrier replaces a sleep here too.
	select {
	case got := <-q.details:
		if got != "full" {
			t.Errorf("detail = %q, want full", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first ask never reached the querier")
	}
	go func() {
		p, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		results <- result{p, ok}
	}()

	close(q.block)
	var got []result
	for range 2 {
		select {
		case res := <-results:
			got = append(got, res)
		case <-time.After(3 * time.Second):
			t.Fatal("an ask never returned")
		}
	}

	if calls := q.calls.Load(); calls != 1 {
		t.Errorf("querier called %d times, want exactly 1 — closely-spaced asks collapse to one round trip", calls)
	}
	for i, res := range got {
		if !res.ok {
			t.Errorf("ask %d refused; both must be answered from the one result", i)
		}
		if res.payload.TotalTokens != 31337 {
			t.Errorf("ask %d payload = %+v, want the flight's reading", i, res.payload)
		}
	}
}

// TestContextUsageResolver_CollapseWindow is AC-2's second side, both halves of it:
// a second ask just after the first COMPLETED is answered from its result, and one
// past the window earns a fresh round trip. The clock is injected, so the boundary is
// crossed by advancing it rather than by sleeping on it.
func TestContextUsageResolver_CollapseWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		advance   time.Duration
		wantCalls int64
	}{
		{"inside the window reuses the reading", contextUsageCollapseWindow - time.Millisecond, 1},
		{"at the window boundary asks again", contextUsageCollapseWindow, 2},
		{"past the window asks again", contextUsageCollapseWindow + time.Second, 2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := newFakeContextUsageQuerier(true)
			clock := newFakeClock()
			r := ctxUsageResolverFor(t, q, nil, clock)

			if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
				t.Fatal("first ask refused")
			}
			clock.advance(tc.advance)
			if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
				t.Fatal("second ask refused")
			}
			if got := q.calls.Load(); got != tc.wantCalls {
				t.Errorf("querier called %d times, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestContextUsageResolver_CollapsesRefusalsToo pins the decision recorded in the
// plan: a refusal is cached for the window like any other result. The alternative —
// caching only successes — would leave a conversation whose child cannot answer free
// to be re-asked as fast as a client can type.
func TestContextUsageResolver_CollapsesRefusalsToo(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(false)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	for i := range 3 {
		if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); ok {
			t.Fatalf("ask %d succeeded against a refusing querier", i)
		}
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("querier called %d times, want exactly 1 — a refusal collapses like any other result", got)
	}
}

// TestContextUsageResolver_UnresolvableInstallsNoFlight is the containment property
// from the security pass: a conversation the daemon cannot resolve must not create a
// map entry, or a client could mint them by naming ids that do not exist.
//
// It asserts on the FLIGHT MAP rather than on the refusal, because the refusal is
// what a resolver keying the map on the request string would also produce.
func TestContextUsageResolver_UnresolvableInstallsNoFlight(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	for _, id := range []string{"NOT_HOSTED_2431", "", "../../etc/passwd"} {
		if _, ok := r.Get(context.Background(), id); ok {
			t.Errorf("Get(%q) succeeded, want a refusal", id)
		}
	}
	r.mu.Lock()
	n := len(r.flights)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("flight map holds %d entries after unresolvable asks, want 0 — entries are keyed by the resolved id and installed only past resolution", n)
	}
	if got := q.calls.Load(); got != 0 {
		t.Errorf("querier called %d times for unresolvable conversations, want 0", got)
	}
}

// TestContextUsageResolver_FlightKeyedByCanonicalID pins the other half of that
// finding: the key is the id the REGISTRY returned, not the string the client sent.
// The resolver double below accepts a differently-spelled request and canonicalises
// it, which is what a normalising registry would do — a resolver keying on the
// request string would install two flights and make two round trips.
func TestContextUsageResolver_FlightKeyedByCanonicalID(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	resolve := func(string) (contextUsageQuerier, conversations.ConversationID, bool) {
		return q, ctxUsageTestConv, true
	}
	r := newContextUsageResolver(context.Background(), resolve, nil, newFakeClock().now)

	for _, spelling := range []string{"spelling-one", "spelling-two"} {
		if _, ok := r.Get(context.Background(), spelling); !ok {
			t.Fatalf("Get(%q) refused", spelling)
		}
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("querier called %d times, want 1 — two spellings of one conversation collapse together", got)
	}
	r.mu.Lock()
	n := len(r.flights)
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("flight map holds %d entries, want 1 keyed by the canonical id", n)
	}
}

// TestContextUsageResolver_DefersUntilTurnEnds is AC-4: a request arriving while a
// turn is in flight writes NOTHING to the child until that turn ends, and is answered
// after it. The tracker is the real one, so the deferral is pinned against the
// primitive that actually ships.
func TestContextUsageResolver_DefersUntilTurnEnds(t *testing.T) {
	t.Parallel()
	busy := newTurnBusyTracker(func(string) (string, bool) { return "", false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	undo := busy.openForDelivery(string(ctxUsageTestConv))
	if !busy.Busy(string(ctxUsageTestConv)) {
		t.Fatal("tracker did not report the conversation busy")
	}

	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, busy, newFakeClock())

	done := make(chan bool, 1)
	go func() {
		_, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		done <- ok
	}()

	// While the turn is open the child must not be written to at all. A negative is
	// only as good as the window it is observed over; this one is bounded by the
	// tracker's own state, which nothing but `undo` can change.
	select {
	case detail := <-q.details:
		t.Fatalf("querier was asked for %q while a turn was open; the request must wait", detail)
	case <-time.After(150 * time.Millisecond):
	}
	if got := q.calls.Load(); got != 0 {
		t.Fatalf("querier called %d times mid-turn, want 0", got)
	}

	undo()
	select {
	case ok := <-done:
		if !ok {
			t.Error("the deferred request was refused; it must be answered after the turn ends")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the deferred request never completed after the turn closed")
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("querier called %d times after the turn closed, want 1", got)
	}
}

// TestContextUsageResolver_NilTrackerIsNotDereferenced pins the PTY wiring, where the
// tracker is never constructed. WaitIdle carries no nil-receiver guard of its own, so
// the resolver owns this check.
func TestContextUsageResolver_NilTrackerIsNotDereferenced(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); !ok {
		t.Fatal("Get refused with a nil tracker; a nil tracker means nothing is busy")
	}
}

// TestContextUsageResolver_CallerDepartureLeavesFlightRunning is the security pass's
// second MUST FIX, pinned. A flight is SHARED: if it ran under the first caller's
// context, that client disconnecting would cancel a round trip other clients are
// waiting on and — refusals being cached — hold the resulting refusal for the whole
// window. One client's departure must not deny another's answer.
func TestContextUsageResolver_CallerDepartureLeavesFlightRunning(t *testing.T) {
	t.Parallel()
	q := newFakeContextUsageQuerier(true)
	q.block = make(chan struct{})
	r := ctxUsageResolverFor(t, q, nil, newFakeClock())

	leaver, cancelLeaver := context.WithCancel(context.Background())
	left := make(chan bool, 1)
	go func() {
		_, ok := r.Get(leaver, string(ctxUsageTestConv))
		left <- ok
	}()
	select {
	case <-q.details:
	case <-time.After(3 * time.Second):
		t.Fatal("the first ask never reached the querier")
	}

	// A second client joins the flight the first installed, then the first leaves.
	stayed := make(chan bool, 1)
	go func() {
		_, ok := r.Get(context.Background(), string(ctxUsageTestConv))
		stayed <- ok
	}()
	cancelLeaver()
	select {
	case ok := <-left:
		if ok {
			t.Error("the departing caller reported success; its own ctx ended first")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the departing caller never returned")
	}

	// The flight itself must still be alive: releasing the child now answers the
	// caller that stayed.
	close(q.block)
	select {
	case ok := <-stayed:
		if !ok {
			t.Error("the remaining caller was refused — one client's departure cancelled a shared flight")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the remaining caller never returned; the flight died with the departing caller")
	}
	if got := q.ctxErr.Load(); got {
		t.Error("the query's context was cancelled by a caller's departure; the flight runs under the daemon context")
	}
}

// TestContextUsageResolver_NilSeamsRefuse pins the unwired postures — foreground and
// v1 leave these nil — as refusals rather than panics.
func TestContextUsageResolver_NilSeamsRefuse(t *testing.T) {
	t.Parallel()
	var nilResolver *contextUsageResolver
	if _, ok := nilResolver.Get(context.Background(), string(ctxUsageTestConv)); ok {
		t.Error("a nil resolver answered a request")
	}
	r := newContextUsageResolver(context.Background(), nil, nil, nil)
	if _, ok := r.Get(context.Background(), string(ctxUsageTestConv)); ok {
		t.Error("a resolver with no conversation source answered a request")
	}
}
