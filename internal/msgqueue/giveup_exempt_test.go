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

// #1014 AC-1 (reset semantics): a real-failure streak that precedes a pending
// hold is discarded — the pending window resets the give-up clock, so neither
// non-pending window alone crosses the bound and give-up never fires. Without the
// reset, the pre-pending streak would survive and trip give-up mid second window.
func TestGiveUpExempt_PendingResetsStreak(t *testing.T) {
	t.Parallel()
	const (
		giveUpAfter = 80 * time.Millisecond
		phaseA      = 30 * time.Millisecond // real failure, < bound
		phaseB      = 40 * time.Millisecond // pending hold, resets the clock
		phaseC      = 30 * time.Millisecond // real failure again, < bound
	)
	d := &scriptDeliver{err: errRealFail}

	gaveUp := make(chan struct{}, 1)
	q, err := New(Config{
		Deliver:       d.deliver,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   giveUpAfter,
		Pending:       exemptPending,
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
	time.Sleep(phaseA) // build a real-failure streak (< bound)
	d.setErr(errTrustPending)
	time.Sleep(phaseB) // legitimate hold: resets the give-up clock
	d.setErr(errRealFail)
	time.Sleep(phaseC) // fresh real-failure streak (< bound)

	// The combined real-failure exposure (phaseA + phaseC) exceeds the bound, but
	// the pending window reset the clock between them, so neither streak alone
	// crosses it and give-up must not have fired.
	select {
	case <-gaveUp:
		t.Fatal("OnGiveUp fired: the pending window did not reset the pre-pending failure streak")
	default:
	}

	// The head is still alive and delivers once the failure clears — proof the
	// drain held rather than abandoned it.
	d.setErr(nil)
	waitDelivered(t, d, "m")

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
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
