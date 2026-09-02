package msgqueue

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turncommit"
)

// errFake is the forced delivery failure the lossless-retry scenario injects.
var errFake = errors.New("fake deliver failure")

// fakeDeliver is a test double for DeliverFunc. It records the order and
// payloads of successful deliveries, enforces that no conversation ever has more
// than one in-flight delivery (the serial-drain invariant), and can simulate
// "claude busy mid-turn" (a per-conversation gate the test releases) and "claude
// unavailable" (a per-conversation forced-failure count). It signals delivery
// start on entered and successful completion on completed so tests synchronise
// without sleeps.
type fakeDeliver struct {
	mu          sync.Mutex
	order       []string        // texts delivered successfully, in order
	inflight    map[string]int  // per-conv in-flight count
	maxInflight int             // max in-flight observed across all conversations
	failTimes   map[string]int  // per-conv remaining forced failures
	permaFail   map[string]bool // per-conv permanent forced-failure toggle
	gates       map[string]chan struct{}
	// commitGates optionally holds the COMMITTING window open: when set for a conv,
	// deliver claims the head via the ctx commit gate, signals on claimed, then
	// blocks here until the test releases it — the window in which the head is
	// un-droppable. Unset (the default) means claim-then-commit with no window.
	commitGates map[string]chan struct{}

	entered   chan string // text, sent at the top of every deliver call
	claimed   chan string // text, sent after the commit gate claims the head
	completed chan string // text, sent after every successful delivery
}

func newFakeDeliver() *fakeDeliver {
	return &fakeDeliver{
		inflight:    map[string]int{},
		failTimes:   map[string]int{},
		permaFail:   map[string]bool{},
		gates:       map[string]chan struct{}{},
		commitGates: map[string]chan struct{}{},
		entered:     make(chan string, 64),
		claimed:     make(chan string, 64),
		completed:   make(chan string, 64),
	}
}

// setPermaFail toggles a conversation into (or out of) permanent delivery
// failure — every deliver returns errFake while on. It models a claude session
// wedged at startup that never commits a turn, letting a test drive the drain's
// give-up path and then clear the wedge to prove a respawn drains.
func (f *fakeDeliver) setPermaFail(convID string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.permaFail[convID] = on
}

func (f *fakeDeliver) deliver(ctx context.Context, convID string, payload []byte) error {
	text := string(payload)

	f.mu.Lock()
	f.inflight[convID]++
	if f.inflight[convID] > f.maxInflight {
		f.maxInflight = f.inflight[convID]
	}
	gate := f.gates[convID]
	f.mu.Unlock()

	f.entered <- text

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			f.inflight[convID]--
			f.mu.Unlock()
			return ctx.Err()
		}
	}

	// waitReady passed → claim the head via the commit gate the queue supplies on
	// the delivery ctx (models the supervisor between waitReady and the write). A
	// false claim means the head was dropped racing the commit, so abort without
	// recording it. A nil gate means no queue gating (not exercised here).
	if g := turncommit.From(ctx); g != nil {
		if !g() {
			f.mu.Lock()
			f.inflight[convID]--
			f.mu.Unlock()
			return turncommit.ErrDropped
		}
		f.mu.Lock()
		commitHold := f.commitGates[convID]
		f.mu.Unlock()
		if commitHold != nil {
			f.claimed <- text
			select {
			case <-commitHold:
			case <-ctx.Done():
				f.mu.Lock()
				f.inflight[convID]--
				f.mu.Unlock()
				return ctx.Err()
			}
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight[convID]--
	if f.permaFail[convID] {
		return errFake // wedged session: never commits, drives give-up.
	}
	if f.failTimes[convID] > 0 {
		f.failTimes[convID]--
		return errFake
	}
	f.order = append(f.order, text)
	f.completed <- text
	return nil
}

func (f *fakeDeliver) deliveredOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

func (f *fakeDeliver) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// recvWithin receives one value from ch or fails after a generous deadline so a
// hung drain fails loudly instead of blocking the suite.
func recvWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// AC #2, #3, #5: a message enqueued while a turn is in flight is held; the
// backlog drains one at a time, in enqueue order, never more than one in flight.
func TestQueue_EnqueueDuringInFlight_DrainsOrderedOneAtATime(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // gate every delivery: simulate an in-flight turn

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	if id := q.Enqueue("c", "m1"); id != 1 {
		t.Fatalf("Enqueue m1 id = %d, want 1", id)
	}
	if id := q.Enqueue("c", "m2"); id != 2 {
		t.Fatalf("Enqueue m2 id = %d, want 2", id)
	}
	if id := q.Enqueue("c", "m3"); id != 3 {
		t.Fatalf("Enqueue m3 id = %d, want 3", id)
	}

	for _, want := range []string{"m1", "m2", "m3"} {
		if got := recvWithin(t, f.entered, "deliver("+want+")"); got != want {
			t.Fatalf("deliver entered for %q, want %q", got, want)
		}
		// While this delivery is gated (in flight), no later delivery may start.
		select {
		case got := <-f.entered:
			t.Fatalf("delivery for %q started while %q in flight", got, want)
		default:
		}
		f.gates["c"] <- struct{}{} // turn ends → this delivery commits
	}

	for range []string{"m1", "m2", "m3"} {
		recvWithin(t, f.completed, "completion")
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m1", "m2", "m3"}) {
		t.Fatalf("delivery order = %v, want [m1 m2 m3]", got)
	}
	if n := f.maxConcurrent(); n != 1 {
		t.Fatalf("max in-flight per conversation = %d, want 1", n)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #4: an empty queue drains as a no-op — Run does nothing and returns cleanly
// on cancel, with zero deliveries.
func TestQueue_EmptyQueue_NoOp(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if got := f.deliveredOrder(); len(got) != 0 {
		t.Fatalf("deliveries = %v, want none", got)
	}
}

// AC #1: queues for different conversations are independent — a conversation
// whose delivery is blocked never blocks or reorders another's.
func TestQueue_PerConversationIndependence(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["a"] = make(chan struct{}) // conv a stays blocked for the whole test

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("a", "a1") // blocked on its gate forever
	q.Enqueue("b", "b1")
	q.Enqueue("b", "b2")
	q.Enqueue("b", "b3")

	var bOrder []string
	for len(bOrder) < 3 {
		got := recvWithin(t, f.completed, "conv b completion")
		if got == "a1" {
			t.Fatalf("conv a delivered %q while gated — conversations not independent", got)
		}
		bOrder = append(bOrder, got)
	}
	if !equalStrings(bOrder, []string{"b1", "b2", "b3"}) {
		t.Fatalf("conv b delivery order = %v, want [b1 b2 b3]", bOrder)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #3: a message enqueued while claude is already idle drains promptly, with no
// external turn-end signal.
func TestQueue_IdleDrainsPromptly(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver() // no gates: claude is idle
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "only")
	if got := recvWithin(t, f.completed, "delivery"); got != "only" {
		t.Fatalf("delivered %q, want only", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #4 + restart pin: a delivery that keeps failing (claude unavailable during a
// child respawn) is retried at the FIFO head until it succeeds — delivered
// exactly once, never dropped.
func TestQueue_LosslessRetry_SurvivesRespawn(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.failTimes["c"] = 3 // first 3 attempts fail, 4th succeeds

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m")
	if got := recvWithin(t, f.completed, "eventual delivery"); got != "m" {
		t.Fatalf("delivered %q, want m", got)
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m"}) {
		t.Fatalf("delivery order = %v, want [m] (exactly once)", got)
	}

	// Each attempt (3 failures + 1 success) sent on entered; all are buffered by
	// the time the success completion arrives.
	attempts := 0
	for {
		select {
		case <-f.entered:
			attempts++
			continue
		default:
		}
		break
	}
	if attempts != 4 {
		t.Fatalf("deliver attempts = %d, want 4 (3 retries + success)", attempts)
	}
	if n := f.maxConcurrent(); n != 1 {
		t.Fatalf("max in-flight = %d, want 1 (retries are serial)", n)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #1000 AC1 + AC3: a head that fails delivery persistently is abandoned after
// the give-up bound instead of retried forever — the drain drops the head and
// fires OnGiveUp with the conversation id and a non-empty daemon-generated
// reason. The message is never delivered.
func TestQueue_GivesUpAfterPersistentFailure(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.setPermaFail("c", true) // claude wedged: every delivery fails

	type giveUp struct{ convID, reason string }
	gaveUp := make(chan giveUp, 4)
	q, err := New(Config{
		Deliver:       f.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   20 * time.Millisecond, // well past a couple of retries
		OnGiveUp:      func(convID, reason string) { gaveUp <- giveUp{convID, reason} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "wedged")

	got := recvWithin(t, gaveUp, "give-up notification")
	if got.convID != "c" {
		t.Fatalf("give-up convID = %q, want c", got.convID)
	}
	if got.reason == "" {
		t.Fatal("give-up reason is empty, want a non-empty daemon-generated reason")
	}
	// The abandoned head was dropped: the backlog is now empty and the message
	// was never delivered.
	waitSnapshotEmpty(t, q, "c")
	if delivered := f.deliveredOrder(); len(delivered) != 0 {
		t.Fatalf("deliveries = %v, want none (wedged head never delivered)", delivered)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #1000 AC2: a transient failure that clears within the give-up bound (an
// ordinary claude-child respawn window) does NOT trip give-up. This is the
// lossless-retry survives-respawn scenario extended with the give-up seam wired
// to a recorder that must never fire.
func TestQueue_TransientFailure_NoGiveUp(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.failTimes["c"] = 3 // first 3 attempts fail, 4th succeeds — a respawn window

	gaveUp := make(chan struct{}, 1)
	q, err := New(Config{
		Deliver:       f.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   5 * time.Second, // comfortably wider than 3 retries
		OnGiveUp:      func(convID, reason string) { gaveUp <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m")
	if got := recvWithin(t, f.completed, "eventual delivery"); got != "m" {
		t.Fatalf("delivered %q, want m", got)
	}
	// Delivery succeeded before the bound elapsed, so give-up must not have fired.
	select {
	case <-gaveUp:
		t.Fatal("OnGiveUp fired on a transient failure that cleared — give-up bound too narrow")
	default:
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #1000 AC2 regression guard: the default give-up bound is wide enough to clear
// a full claude-child respawn/backoff window. The supervisor's max backoff
// window is 30s BackoffMax / 60s BackoffReset (internal/supervisor/supervisor.go);
// those are unexported New() defaults, not exported consts, so this pins the
// numeric floor directly and guards against a silent narrowing below it.
func TestQueue_DefaultGiveUpAfter_ExceedsBackoffWindow(t *testing.T) {
	t.Parallel()
	if defaultGiveUpAfter < 2*time.Minute {
		t.Fatalf("defaultGiveUpAfter = %v, want >= 2m (must exceed the 60s BackoffReset window)", defaultGiveUpAfter)
	}
}

// #1000 AC4: the give-up path preserves the NEVER-log-head.text discipline. The
// distinctive marker fails the assertion if the give-up log line or the reason
// string ever interpolated the untrusted queued message text.
func TestQueue_GiveUp_NoUntrustedContentLeak(t *testing.T) {
	t.Parallel()
	const secret = "SUPER_SECRET_PHONE_TEXT"
	f := newFakeDeliver()
	f.setPermaFail("c", true)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	reasons := make(chan string, 1)
	q, err := New(Config{
		Deliver:       f.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   20 * time.Millisecond,
		Logger:        logger,
		OnGiveUp:      func(convID, reason string) { reasons <- reason },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", secret)

	// Receiving the reason happens-after every give-up-path log write (same drain
	// goroutine, then the channel send), so reading buf here is race-clean.
	reason := recvWithin(t, reasons, "give-up notification")
	if strings.Contains(reason, secret) {
		t.Fatalf("give-up reason leaked untrusted message text: %q", reason)
	}
	if logs := buf.String(); strings.Contains(logs, secret) {
		t.Fatalf("log output leaked untrusted message text: %q", logs)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #1000 AC5: after give-up the drain exits clean — draining is cleared and the
// head dropped — and a later Enqueue respawns the drain. Cancelling proves
// wg.Wait unblocked (no leaked drain goroutine).
func TestQueue_GiveUp_CleanTeardownAndRespawn(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.setPermaFail("c", true)

	gaveUp := make(chan struct{}, 1)
	q, err := New(Config{
		Deliver:       f.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   20 * time.Millisecond,
		OnGiveUp:      func(convID, reason string) { gaveUp <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "wedged")
	recvWithin(t, gaveUp, "give-up notification")
	// Give-up clears draining and drops the head before firing the seam, so by now
	// the conversation is back to a clean idle state.
	waitSnapshotEmpty(t, q, "c")

	// Clear the wedge and enqueue again: the old drain exited, so this Enqueue must
	// respawn a fresh drain and deliver.
	f.setPermaFail("c", false)
	q.Enqueue("c", "after")
	if got := recvWithin(t, f.completed, "delivery after respawn"); got != "after" {
		t.Fatalf("delivered %q, want after (drain must respawn after give-up)", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// AC #1: ids are stable, monotonic from 1 within a conversation, and independent
// across conversations. Enqueue assigns ids without Run.
func TestQueue_StableIndependentIDs(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if id := q.Enqueue("a", "x"); id != 1 {
		t.Fatalf("a/x id = %d, want 1", id)
	}
	if id := q.Enqueue("a", "y"); id != 2 {
		t.Fatalf("a/y id = %d, want 2", id)
	}
	if id := q.Enqueue("b", "z"); id != 1 {
		t.Fatalf("b/z id = %d, want 1 (independent counter)", id)
	}
	if id := q.Enqueue("a", "w"); id != 3 {
		t.Fatalf("a/w id = %d, want 3", id)
	}
	if id := q.Enqueue("b", "q"); id != 2 {
		t.Fatalf("b/q id = %d, want 2", id)
	}
}

// Clean shutdown: cancelling ctx while a drain is blocked in deliver makes Run
// return — wg.Wait() unblocking is the proof that every drain goroutine exited.
func TestQueue_CleanShutdown_NoLeak(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // never released: the drain blocks in deliver

	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m")
	recvWithin(t, f.entered, "drain to enter deliver") // ensure the drain is blocked in deliver

	cancel()
	if err := recvWithin(t, runErr, "Run to return after cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// New rejects a nil delivery seam — it is caller-supplied wiring, surfaced as an
// error rather than a panic.
func TestNew_RejectsNilDeliver(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with nil Deliver returned nil error, want non-nil")
	}
}

// #869 AC #1, #2, #5: Enqueue caps the per-conversation backlog. With the cap's
// worth already queued, a further Enqueue returns 0 without appending; the
// existing backlog (ids + order) is untouched and the rejected message never
// appears in Snapshot.
func TestQueue_CapEnforcedAtEnqueue_RejectNeverDrops(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Below the cap Enqueue behaves as today: appends, returns a dense id >= 1.
	if id := q.Enqueue("a", "m1"); id != 1 {
		t.Fatalf("Enqueue m1 id = %d, want 1", id)
	}
	if id := q.Enqueue("a", "m2"); id != 2 {
		t.Fatalf("Enqueue m2 id = %d, want 2", id)
	}
	// At the cap the third Enqueue is rejected: returns 0, appends nothing.
	if id := q.Enqueue("a", "m3"); id != 0 {
		t.Fatalf("Enqueue m3 at cap id = %d, want 0 (rejected)", id)
	}

	// Reject, never drop: the backlog still holds exactly the two accepted
	// messages in enqueue order; the rejected one never entered it, and the
	// oldest was not evicted to make room.
	snap := q.Snapshot("a")
	if len(snap) != 2 {
		t.Fatalf("Snapshot len = %d, want 2 (backlog untouched by reject)", len(snap))
	}
	if snap[0].ID != 1 || snap[0].Text != "m1" || snap[1].ID != 2 || snap[1].Text != "m2" {
		t.Fatalf("Snapshot = %+v, want ids [1 2] texts [m1 m2] in order", snap)
	}
}

// #869 AC #2, #5: after a rejected Enqueue, the two accepted messages still
// drain in enqueue order and the rejected one is never delivered. Filling the
// backlog before Run keeps it full while we fill (nothing drains yet).
func TestQueue_RejectedMessage_NeverDrains(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "m1")
	q.Enqueue("a", "m2")
	if id := q.Enqueue("a", "m3"); id != 0 {
		t.Fatalf("Enqueue m3 at cap id = %d, want 0 (rejected)", id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	for _, want := range []string{"m1", "m2"} {
		if got := recvWithin(t, f.completed, "delivery of "+want); got != want {
			t.Fatalf("delivered %q, want %q", got, want)
		}
	}
	if got := f.deliveredOrder(); !equalStrings(got, []string{"m1", "m2"}) {
		t.Fatalf("delivery order = %v, want [m1 m2] (rejected m3 never delivered)", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #869 AC #4: the cap is per conversation. Filling A to the cap so it rejects
// does not affect B — B enqueues normally from its own independent counter.
func TestQueue_CapIsPerConversation(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "a1")
	q.Enqueue("a", "a2")
	if id := q.Enqueue("a", "a3"); id != 0 {
		t.Fatalf("A at cap id = %d, want 0 (rejected)", id)
	}

	if id := q.Enqueue("b", "b1"); id != 1 {
		t.Fatalf("B enqueue id = %d, want 1 (independent of a filled A)", id)
	}
	if id := q.Enqueue("b", "b2"); id != 2 {
		t.Fatalf("B enqueue id = %d, want 2", id)
	}
}

// #869: a rejected Enqueue consumes no id. After a reject, freeing a slot with
// Remove lets the next Enqueue succeed and continue the DENSE id sequence — the
// rejected attempt left no gap.
func TestQueue_RejectConsumesNoID_RemoveFreesSlot(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver, MaxQueuedPerConversation: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "m1") // id 1
	q.Enqueue("a", "m2") // id 2
	if id := q.Enqueue("a", "m3"); id != 0 {
		t.Fatalf("Enqueue m3 at cap id = %d, want 0 (rejected)", id)
	}

	// No drain runs (Run not started), so id 2 is a non-head, non-in-flight
	// message and is removable — this frees a backlog slot.
	if !q.Remove("a", 2) {
		t.Fatal("Remove(a, 2) = false, want true (non-head, not in flight)")
	}

	// The next Enqueue succeeds and its id is 3, not 4: the rejected m3 consumed
	// no id, so the accepted sequence stays dense.
	if id := q.Enqueue("a", "m4"); id != 3 {
		t.Fatalf("Enqueue m4 id = %d, want 3 (reject consumed no id)", id)
	}
}

// #869: an unset MaxQueuedPerConversation resolves to the default cap in New,
// and that default fires at exactly defaultMaxQueuedPerConversation — small
// backlogs (every existing scenario) never reject. This is the regression guard
// that the ~45 existing Enqueue call sites keep passing unchanged.
func TestQueue_DefaultCap_ResolvesAndFires(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver}) // MaxQueuedPerConversation unset ⇒ default
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 1; i <= defaultMaxQueuedPerConversation; i++ {
		if id := q.Enqueue("a", "m"); id != uint64(i) {
			t.Fatalf("Enqueue #%d id = %d, want %d (accepted under default cap)", i, id, i)
		}
	}
	if id := q.Enqueue("a", "over"); id != 0 {
		t.Fatalf("Enqueue past default cap id = %d, want 0 (rejected)", id)
	}
}

// equalMessages compares two backlogs by id, text, and ts (via time.Time.Equal,
// since JSON round-trips strip the monotonic reading — the project-wide rule).
func equalMessages(a, b []QueuedMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Text != b[i].Text || !a[i].TS.Equal(b[i].TS) {
			return false
		}
	}
	return true
}

// waitSnapshotEmpty polls until convID's backlog is empty (its drain confirmed
// the last delivery and advanceLocked dropped the head) or the deadline expires.
// The synchronisation knob for the drained-conversation scenario: f.completed
// fires inside deliver, one advanceLocked before the head actually leaves items.
func waitSnapshotEmpty(t *testing.T, q *Queue, convID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(q.Snapshot(convID)) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("conversation %q did not drain empty", convID)
}

// #878: SnapshotAll enumerates every conversation holding a backlog, keyed by
// conversation id, each value mirroring that conversation's Snapshot (order, id,
// text, ts). No Run: enqueue-only leaves both backlogs intact.
func TestQueue_SnapshotAll_TwoConversations(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "a1")
	q.Enqueue("a", "a2")
	q.Enqueue("b", "b1")

	all := q.SnapshotAll()
	if len(all) != 2 {
		t.Fatalf("SnapshotAll len = %d, want 2 (both conversations have a backlog)", len(all))
	}
	for _, convID := range []string{"a", "b"} {
		got, ok := all[convID]
		if !ok {
			t.Fatalf("SnapshotAll missing key %q", convID)
		}
		if want := q.Snapshot(convID); !equalMessages(got, want) {
			t.Errorf("SnapshotAll[%q] = %+v, want %+v (must mirror Snapshot)", convID, got, want)
		}
	}
	if all["a"][0].ID != 1 || all["a"][0].Text != "a1" || all["a"][1].ID != 2 || all["a"][1].Text != "a2" {
		t.Errorf("SnapshotAll[a] = %+v, want ids [1 2] texts [a1 a2] in order", all["a"])
	}
}

// #878 AC3: a conversation drained to empty keeps its convQueue entry (items ==
// nil) but is OMITTED from SnapshotAll. A gated conversation held non-empty proves
// the omission is the empty-skip, not a trivially empty map.
func TestQueue_SnapshotAll_OmitsDrainedConversation(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["held"] = make(chan struct{}) // its head blocks in deliver for the whole test

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("held", "h1") // stays in flight (gated), backlog never empties
	q.Enqueue("gone", "g1") // drains to completion, leaving an empty-but-retained convQueue

	if got := recvWithin(t, f.completed, "delivery of g1"); got != "g1" {
		t.Fatalf("delivered %q, want g1", got)
	}
	waitSnapshotEmpty(t, q, "gone") // wait for advanceLocked to drop the head

	all := q.SnapshotAll()
	if _, ok := all["gone"]; ok {
		t.Errorf("SnapshotAll includes drained conversation %q, want it omitted (AC3)", "gone")
	}
	held, ok := all["held"]
	if !ok {
		t.Fatalf("SnapshotAll missing the still-backlogged conversation %q", "held")
	}
	if len(held) != 1 || held[0].ID != 1 || held[0].Text != "h1" {
		t.Errorf("SnapshotAll[held] = %+v, want the single in-flight head {1 h1}", held)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #878: a fresh queue with no conversations yields a non-nil empty map (not nil).
func TestQueue_SnapshotAll_EmptyQueue(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	all := q.SnapshotAll()
	if all == nil {
		t.Fatal("SnapshotAll returned nil, want a non-nil empty map")
	}
	if len(all) != 0 {
		t.Errorf("SnapshotAll len = %d, want 0 (no conversations)", len(all))
	}
}

// #878: the in-flight (mid-delivery) head is part of the backlog, so SnapshotAll
// includes it — mirroring Snapshot's head-included semantics.
func TestQueue_SnapshotAll_IncludesInFlightHead(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // hold the head in flight

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m1") // becomes the gated in-flight head
	q.Enqueue("c", "m2")

	if got := recvWithin(t, f.entered, "delivery start of m1"); got != "m1" {
		t.Fatalf("first deliver entered with %q, want m1", got)
	}

	got := q.SnapshotAll()["c"]
	if len(got) != 2 || got[0].ID != 1 || got[0].Text != "m1" || got[1].ID != 2 || got[1].Text != "m2" {
		t.Errorf("SnapshotAll[c] = %+v, want [{1 m1} {2 m2}] including the in-flight head", got)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// #878: the returned slices are value copies — mutating one does not change engine
// state observed by a later Snapshot/SnapshotAll.
func TestQueue_SnapshotAll_ReturnsValueCopies(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	q, err := New(Config{Deliver: f.deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("a", "original")

	first := q.SnapshotAll()
	first["a"][0].Text = "mutated" // scribble on the returned copy
	first["a"] = append(first["a"], QueuedMessage{ID: 99, Text: "injected"})

	if again := q.Snapshot("a"); len(again) != 1 || again[0].Text != "original" {
		t.Errorf("Snapshot after mutating SnapshotAll result = %+v, want the untouched [{1 original}]", again)
	}
	if again := q.SnapshotAll()["a"]; len(again) != 1 || again[0].Text != "original" {
		t.Errorf("SnapshotAll after mutating a prior result = %+v, want the untouched [{1 original}]", again)
	}
}

// #878 -race: SnapshotAll concurrent with Enqueue and the drain must be clean.
// SnapshotAll takes the same q.mu leaf lock as Enqueue/Snapshot/advanceLocked, so
// -race must report nothing.
func TestQueue_SnapshotAll_RaceWithEnqueueAndDrain(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver() // no gates: drains run promptly, racing the snapshots
	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	// Continuously drain the fake's signal channels so deliveries never wedge on
	// the bounded entered/completed buffers, 64 each. This race test fires 400
	// enqueues but consumes no per-delivery signal; a full entered buffer would
	// block deliver mid-send, and that send is not ctx-aware, so cancel() could
	// not free it and Run's wg.Wait would hang. Kept alive until after Run
	// returns so no final post-cancel delivery can block either.
	drainStop := make(chan struct{})
	go func() {
		for {
			select {
			case <-f.entered:
			case <-f.completed:
			case <-drainStop:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			conv := string(rune('a' + w))
			for i := 0; i < 100; i++ {
				q.Enqueue(conv, "x")
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = q.SnapshotAll()
			}
		}()
	}
	wg.Wait()

	cancel()
	err = <-runErr
	close(drainStop)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// TestQueue_RemoveHeadWaitingForIdle_Droppable proves the #487 fix: a message
// queued BEHIND a running turn — the drain is parked inside deliver() waiting for
// claude to go idle, so the message is the current head but has NOT been written
// yet — must be droppable. Before the fix Remove refused ANY idx==0 head while a
// drain goroutine was alive (draining), so dequeue_message silently no-oped for
// exactly the queue-while-busy-then-drop case: the head is the message a user
// queued precisely so they could still cancel it. The fix distinguishes "waiting
// for idle" (droppable) from "actively committing the write" (un-droppable).
func TestQueue_RemoveHeadWaitingForIdle_Droppable(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // gate every delivery: models claude busy mid-turn

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m1")
	q.Enqueue("c", "m2")

	// m1 delivers (gated); release it so it commits and advances out of the FIFO.
	if got := recvWithin(t, f.entered, "deliver(m1)"); got != "m1" {
		t.Fatalf("first delivery = %q, want m1", got)
	}
	f.gates["c"] <- struct{}{}
	recvWithin(t, f.completed, "m1 completion")

	// m2 now delivers but blocks on the gate — the drain is parked waiting for
	// claude to go idle. m2 is the head, but has not been written.
	if got := recvWithin(t, f.entered, "deliver(m2)"); got != "m2" {
		t.Fatalf("second delivery = %q, want m2", got)
	}

	// The whole point: dropping m2 while it waits must succeed.
	if !q.Remove("c", 2) {
		t.Fatalf("Remove(m2) = false; a message waiting behind a running turn must be droppable (#487)")
	}

	// m2 must leave the backlog and never be delivered: Remove cancels the pending
	// delivery, the drain re-loops and finds the FIFO empty.
	deadline := time.Now().Add(2 * time.Second)
	for len(q.Snapshot("c")) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("m2 still in backlog after drop: %v", q.Snapshot("c"))
		}
		time.Sleep(time.Millisecond)
	}
	if order := f.deliveredOrder(); !equalStrings(order, []string{"m1"}) {
		t.Fatalf("delivered = %v, want [m1] only (m2 dropped before it ran)", order)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// deliveryText and deliveryPayload are the two halves of the #2038 seam, kept
// DELIBERATELY DIFFERENT so every assertion below is non-vacuous: a build that
// delivered the queued text, or snapshotted the delivered payload, answers the
// other string and fails. The payload is shaped like the composed prompt the
// send_message handler builds — a host path is exactly what must not reach a
// snapshot.
const (
	deliveryText    = "look at this"
	deliveryPayload = "look at this\n\nAttached files: /home/u/.pyry/x/conversations/c/attachments/a/report.pdf"
)

// TestQueue_EnqueueDelivery_DeliversPayloadAndSnapshotsText is the #2038 seam's
// core pin: what reaches DeliverFunc is the delivery payload, while what Snapshot
// and SnapshotAll report is the queued text. Both reads happen while the delivery
// is GATED — an item leaves the backlog on a confirmed commit, so the snapshot
// window only exists mid-flight.
func TestQueue_EnqueueDelivery_DeliversPayloadAndSnapshotsText(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{}) // hold the delivery so the backlog stays readable

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	if id := q.EnqueueDelivery("c", deliveryText, deliveryPayload); id != 1 {
		t.Fatalf("EnqueueDelivery id = %d, want 1", id)
	}
	if got := recvWithin(t, f.entered, "deliver"); got != deliveryPayload {
		t.Fatalf("DeliverFunc payload = %q, want the delivery payload %q", got, deliveryPayload)
	}

	snap := q.Snapshot("c")
	if len(snap) != 1 {
		t.Fatalf("Snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Text != deliveryText {
		t.Errorf("Snapshot Text = %q, want the queued text %q", snap[0].Text, deliveryText)
	}
	all := q.SnapshotAll()
	if len(all["c"]) != 1 || all["c"][0].Text != deliveryText {
		t.Errorf("SnapshotAll()[\"c\"] = %+v, want one entry carrying %q", all["c"], deliveryText)
	}

	f.gates["c"] <- struct{}{}
	recvWithin(t, f.completed, "completion")

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// TestQueue_Enqueue_DeliversTheSnapshotText pins the compatibility half: Enqueue
// is EnqueueDelivery with the two halves equal, so what claude receives is
// byte-for-byte what a client reads back. This is #2038 AC 2 at the engine — the
// 108 existing Enqueue call sites depend on it and none of them says so.
func TestQueue_Enqueue_DeliversTheSnapshotText(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{})

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", deliveryText)
	if got := recvWithin(t, f.entered, "deliver"); got != deliveryText {
		t.Fatalf("DeliverFunc payload = %q, want %q", got, deliveryText)
	}
	if snap := q.Snapshot("c"); len(snap) != 1 || snap[0].Text != deliveryText {
		t.Fatalf("Snapshot = %+v, want one entry carrying %q", snap, deliveryText)
	}

	f.gates["c"] <- struct{}{}
	recvWithin(t, f.completed, "completion")

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// TestQueue_EnqueueDelivery_RejectedAtCap pins that the #869 per-conversation
// bound is enforced identically on the new entry point — it is the SAME single
// insertion point, so a build that let the new method skip the cap would reopen
// the in-memory DoS surface for every message naming an attachment.
func TestQueue_EnqueueDelivery_RejectedAtCap(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["c"] = make(chan struct{})

	q, err := New(Config{Deliver: f.deliver, RetryInterval: time.Millisecond, MaxQueuedPerConversation: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	if id := q.EnqueueDelivery("c", deliveryText, deliveryPayload); id != 1 {
		t.Fatalf("first EnqueueDelivery id = %d, want 1", id)
	}
	recvWithin(t, f.entered, "deliver")

	if id := q.EnqueueDelivery("c", "second", "second payload"); id != 0 {
		t.Errorf("EnqueueDelivery at cap = %d, want 0 (reject, never drop)", id)
	}
	if snap := q.Snapshot("c"); len(snap) != 1 || snap[0].Text != deliveryText {
		t.Errorf("Snapshot = %+v, want the first message untouched", snap)
	}

	f.gates["c"] <- struct{}{}
	recvWithin(t, f.completed, "completion")

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}
