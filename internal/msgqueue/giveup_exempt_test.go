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
)

// errTrustPending stands in for supervisor.ErrTrustModalPending (which
// internal/msgqueue must not import): the drain-held sentinel the injected
// PendingFunc classifies as a legitimate wait for a remote decision rather than a
// delivery failure. #1014 AC-1.
var errTrustPending = errors.New("trust modal pending")

// errRealFail is a genuine delivery failure the PendingFunc rejects — a wedged
// session that must count toward the give-up bound. It is a distinct sentinel so
// exemptPending's errors.Is is specific: only a real hold is exempt.
var errRealFail = errors.New("real delivery failure")

// exemptPending is the Config.Pending classifier under test. It mirrors
// cmd/pyry's wiring `errors.Is(err, supervisor.ErrTrustModalPending)`: only the
// dedicated hold sentinel is a legitimate wait; every other error is a failure.
func exemptPending(err error) bool { return errors.Is(err, errTrustPending) }

// scriptDeliver is a DeliverFunc double whose per-call return is a
// caller-controlled error (flip via setErr). It records successful deliveries and
// counts attempts so the pending tests synchronise on the drain without reaching
// into queue internals. It shares nothing with queue_test.go's fakeDeliver so
// that file stays untouched (#935 coexistence).
type scriptDeliver struct {
	mu        sync.Mutex
	err       error
	delivered []string
	attempts  int
}

func (d *scriptDeliver) setErr(err error) {
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
}

func (d *scriptDeliver) deliver(_ context.Context, _ string, payload []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attempts++
	if d.err != nil {
		return d.err
	}
	d.delivered = append(d.delivered, string(payload))
	return nil
}

func (d *scriptDeliver) attemptCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attempts
}

func (d *scriptDeliver) deliveredTexts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.delivered...)
}

// waitDelivered polls until want appears in the delivered set or the deadline
// passes. It is the "the held turn ran" assertion.
func waitDelivered(t *testing.T, d *scriptDeliver, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, got := range d.deliveredTexts() {
			if got == want {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("message %q was never delivered", want)
}

// #1014 AC-1: while the trust prompt is legitimately pending the drain holds the
// head WITHOUT counting the window toward the give-up bound — OnGiveUp never
// fires and the head stays queued no matter how long the operator takes. Once
// trust is accepted (delivery succeeds) the held turn runs. Also proves the
// pending Debug log is content-free (never the untrusted queued text).
func TestGiveUpExempt_PendingHeldNeverGivesUp_AcceptRuns(t *testing.T) {
	t.Parallel()
	const (
		secret      = "SECRET_PENDING_PHONE_TEXT"
		giveUpAfter = 40 * time.Millisecond
	)
	d := &scriptDeliver{err: errTrustPending}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	gaveUp := make(chan struct{}, 1)
	q, err := New(Config{
		Deliver:       d.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   giveUpAfter,
		Pending:       exemptPending,
		Logger:        logger,
		OnGiveUp:      func(convID, reason string) { gaveUp <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	start := time.Now()
	q.Enqueue("c", secret)

	// Hold the pending error for several give-up bound-widths: a NON-exempt drain
	// would have abandoned the head many times over by now.
	for time.Since(start) < 3*giveUpAfter {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-gaveUp:
		t.Fatal("OnGiveUp fired while the trust prompt was legitimately pending — the give-up window was not exempted")
	default:
	}
	if snap := q.Snapshot("c"); len(snap) != 1 || snap[0].Text != secret {
		t.Fatalf("held head = %v, want the single queued message still at the FIFO head", snap)
	}
	if n := d.attemptCount(); n < 2 {
		t.Fatalf("deliver attempts = %d, want the drain to have retried the held head", n)
	}

	// Trust accepted → delivery now succeeds → the held turn runs.
	d.setErr(nil)
	waitDelivered(t, d, secret)
	select {
	case <-gaveUp:
		t.Fatal("OnGiveUp fired after the held head was accepted and delivered")
	default:
	}

	// Stop the drain, then read the log race-free (Run joins the drain on return).
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if logs := buf.String(); strings.Contains(logs, secret) {
		t.Fatalf("pending Debug log leaked untrusted queued text: %q", logs)
	}
}

// #1014 AC-1 (the bound stays intact for real wedges): an error the PendingFunc
// rejects still trips the give-up bound — the head is abandoned and OnGiveUp
// fires with a content-free reason. The exemption is specific to the hold
// sentinel; it must not weaken give-up for genuine failures.
func TestGiveUpExempt_NonPendingStillGivesUp(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_WEDGED_PHONE_TEXT"
	d := &scriptDeliver{err: errRealFail}

	type notice struct{ convID, reason string }
	gaveUp := make(chan notice, 1)
	q, err := New(Config{
		Deliver:       d.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   20 * time.Millisecond,
		Pending:       exemptPending,
		OnGiveUp:      func(convID, reason string) { gaveUp <- notice{convID, reason} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", secret)

	got := recvWithin(t, gaveUp, "give-up on a non-pending failure")
	if got.convID != "c" {
		t.Fatalf("give-up convID = %q, want c", got.convID)
	}
	if got.reason == "" {
		t.Fatal("give-up reason is empty, want a non-empty daemon-generated reason")
	}
	if strings.Contains(got.reason, secret) {
		t.Fatalf("give-up reason leaked untrusted queued text: %q", got.reason)
	}
	waitSnapshotEmpty(t, q, "c")
	if delivered := d.deliveredTexts(); len(delivered) != 0 {
		t.Fatalf("deliveries = %v, want none (wedged head never delivered)", delivered)
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// A processed pending result resets the same head's failure streak. Each real
// streak stays below the elapsed bound while the original start expires.
func TestGiveUpExempt_PendingResetsStreak(t *testing.T) {
	t.Parallel()
	const giveUpAfter = 80 * time.Millisecond
	type step struct {
		name string
		at   time.Duration
		err  error
	}
	steps := []step{
		{"first failure", 0, errRealFail},
		{"first streak", 30 * time.Millisecond, errRealFail},
		{"pending", 40 * time.Millisecond, errTrustPending},
		{"fresh failure", 100 * time.Millisecond, errRealFail},
		{"second streak", 130 * time.Millisecond, errRealFail},
		{"recovery", 140 * time.Millisecond, nil},
	}
	firstStreak := steps[1].at - steps[0].at
	secondStreak := steps[4].at - steps[3].at
	withoutReset := steps[4].at - steps[0].at
	if firstStreak >= giveUpAfter || secondStreak >= giveUpAfter || steps[3].at-steps[0].at <= giveUpAfter {
		t.Fatal("script must keep separate streaks below the bound and expire the original start before recovery")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	results := make(chan step)
	entered := make(chan QueuedMessage)
	delivered := make(chan QueuedMessage, 1)
	gaveUp := make(chan string, 1)
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	// Only the drain reads or writes now and writes; scheduling consumes no fake time.
	now := base
	writes := 0
	q, err := New(Config{
		Deliver: func(ctx context.Context, _ string, payload []byte) error {
			msg, _ := DeliveryMessage(ctx)
			// Include the actual delivery payload in the acknowledged projection.
			msg.Text = string(payload)
			select {
			case entered <- msg:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case result := <-results:
				now = base.Add(result.at)
				if result.err == nil {
					writes++
				}
				return result.err
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		RetryInterval: time.Millisecond,
		GiveUpAfter:   giveUpAfter,
		Pending:       exemptPending,
		OnGiveUp:      func(convID, reason string) { gaveUp <- reason },
		OnDelivered:   func(_ string, msg QueuedMessage) { delivered <- msg },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q.failureNow = func() time.Time { return now }
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = q.Run(ctx)
		close(done)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
			if !errors.Is(runErr, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", runErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not join its drain after cancellation")
		}
	}
	t.Cleanup(stop)

	id := q.Enqueue("c", "m")
	for i, result := range steps {
		select {
		case reason := <-gaveUp:
			t.Fatalf("OnGiveUp fired before %s: pending did not reset the failure streak: %s", result.name, reason)
		case msg := <-entered:
			if msg.ID != id || msg.Text != "m" {
				t.Fatalf("attempt %s = %+v, want original head %d with payload m", result.name, msg, id)
			}
		case <-ctx.Done():
			t.Fatalf("waiting for attempt %s: %v", result.name, ctx.Err())
		}
		// The next Deliver entry follows all prior result processing and retry,
		// so this acknowledges pending completion, not just classification.
		if i > 0 {
			t.Logf("processed %s at elapsed=%s", steps[i-1].name, steps[i-1].at)
		}
		if snap := q.Snapshot("c"); len(snap) != 1 || snap[0].ID != id || snap[0].Text != "m" {
			t.Fatalf("head before %s = %+v, want original queued message %d", result.name, snap, id)
		}
		select {
		case results <- result:
		case <-ctx.Done():
			t.Fatalf("releasing attempt %s: %v", result.name, ctx.Err())
		}
	}
	select {
	case msg := <-delivered:
		if msg.ID != id || msg.Text != "m" {
			t.Fatalf("delivered = %+v, want original message %d", msg, id)
		}
	case reason := <-gaveUp:
		t.Fatalf("OnGiveUp fired instead of recovery delivery: %s", reason)
	case <-ctx.Done():
		t.Fatalf("message m was never delivered after recovery: %v", ctx.Err())
	}
	stop()
	if writes != 1 || len(delivered) != 0 || len(q.Snapshot("c")) != 0 {
		t.Fatalf("recovery writes=%d, extra delivery callbacks=%d, backlog=%v", writes, len(delivered), q.Snapshot("c"))
	}
	select {
	case reason := <-gaveUp:
		t.Fatalf("OnGiveUp fired despite recovery delivery: %s", reason)
	default:
	}
	t.Logf("processed recovery at elapsed=%s; streaks=%s,%s < bound=%s; without reset=%s > bound; writes=%d; give-ups=0",
		steps[5].at, firstStreak, secondStreak, giveUpAfter, withoutReset, writes)
}

// #1014 AC-1 (opt-in guard): with Pending nil (the pre-#1014 construction) a
// persistently-failing head gives up as before, even for the hold sentinel — the
// exemption branch is opt-in and off by default.
func TestGiveUpExempt_NilPending_GivesUpAsBefore(t *testing.T) {
	t.Parallel()
	d := &scriptDeliver{err: errTrustPending} // no classifier ⇒ counts as a failure

	gaveUp := make(chan struct{}, 1)
	q, err := New(Config{
		Deliver:       d.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   20 * time.Millisecond,
		// Pending: nil — the exemption is not wired.
		OnGiveUp: func(convID, reason string) { gaveUp <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	q.Enqueue("c", "m")
	recvWithin(t, gaveUp, "give-up with no Pending classifier")
	waitSnapshotEmpty(t, q, "c")

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}
